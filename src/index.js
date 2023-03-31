const fs = require('fs');
const express = require('express');
const fetch = require('node-fetch');
const { Storage } = require('@google-cloud/storage');

function tokenAuthHeader(token) {
    return 'Basic ' + Buffer.from("token:" + token).toString('base64');
}

function localFileExists(filePath) {
    return new Promise((resolve, reject) => {
        fs.access(filePath, fs.F_OK, (err) => {
            if (err) {
                // file does not exist
                resolve(false);
            }
            //file exists
            resolve(true);
        })
    });
}

function streamToString (stream) {
    const chunks = [];
    return new Promise((resolve, reject) => {
        stream.on('data', chunk => chunks.push(chunk));
        stream.on('error', reject);
        stream.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')));
    });
}

function waitForStreamToEnd(stream) {
    console.log('waiting for stream to end...');
    return new Promise((resolve, reject) => {
        stream.on('error', reject);
        stream.on('finish', () => {
            console.log('stream finished.');
            resolve();
        });
    });
}

const storage = new Storage();
function bucket() {
    return storage.bucket('github-package-registry-storage');
}

async function getToken(tokenName) {
    if (await localFileExists(tokenName)) {
        return fs.readFileSync(tokenName, 'utf-8').trim();
    } else {
        const stream = await bucket().file('credentials/' + tokenName).createReadStream();
        const data = await streamToString(stream);
        return data.trim();
    }
}

function modifiedHeadersWithAuth(headers, token) {
    const modifiedHeaders = {
        ...headers,
        authorization: tokenAuthHeader(token)
    };
    delete modifiedHeaders.host;
    return modifiedHeaders;
}

function parsePathAsArtifact(path) {
    if (path.length < 4) {
        throw new Error(`The path ${path} is not a valid Maven repository path.`);
    }

    if (path.endsWith("maven-metadata.xml")) {
        const splitted = path.split('/');
        const file = splitted[splitted.length - 1];
        const version = ""
        const artifactId = splitted[splitted.length - 2];
        const groupId = splitted.splice(0, splitted.length - 2).join('.');
        return { groupId, artifactId, version, file };
    } else {
        const splitted = path.split('/');
        const file = splitted[splitted.length - 1];
        const version = splitted[splitted.length - 2];
        const artifactId = splitted[splitted.length - 3];
        const groupId = splitted.splice(0, splitted.length - 3).join('.');
        return { groupId, artifactId, version, file };
    }
}

async function isPackagePublic(parsed, token) {
    const packageName = parsed.groupId + '.' + parsed.artifactId;

    const query = `
        query {
            organization(login:"navikt") {
                packages(first: 1, names: [${JSON.stringify(packageName)}]) {
                    nodes {
                      repository {
                        isPrivate
                      }
                    }
                }
            }
        }
    `;

    const response = await fetch('https://api.github.com/graphql', {
        method: 'post',
        headers: {
            authorization: 'bearer ' + token,
            Accept: 'application/vnd.github.packages-preview+json'
        },
        body: JSON.stringify({
            query
        })
    });

    const result = await response.json();

    if (result.data === undefined || result.data == null) {
        console.error(`Unexpected response from GitHub`, response.status);
        console.error(`Unexpected response from GitHub`, result);
    }

    if (result.data.organization.packages.nodes.length === 0) {
        return {
            error: 'PACKAGE_NOT_FOUND',
            result: false
        };
    }

    if (result.data.organization.packages.nodes[0].repository === undefined || result.data.organization.packages.nodes[0].repository === null) {
        return {
            error: 'PACKAGE_NODE_HAS_NO_REPOSITORY',
            result: false
        };
    } else {
        return {
            result: !result.data.organization.packages.nodes[0].repository.isPrivate
        };
    }
}

async function handleSimple(req, res, repo, path) {
    try {
        const token = await getToken('github-token');

        const parsed = parsePathAsArtifact(path);

        console.info(`parsed: ${JSON.stringify(parsed)} , packageName: ${packageName}`);

        if (!parsed.groupId.startsWith("no.nav")) {
            res.status(404).send(`GroupId does not start with 'no.nav'. Assuming a non NAV package`);
            return;
        }

        const isPackagePublicStatus = await isPackagePublic(parsed, token);
        if (!isPackagePublicStatus.result) {
            if (isPackagePublicStatus.error) {
                console.error(`Could not get package visibility status`, isPackagePublicStatus.error);
            }
            res.status(404).send(`Could not get metadata for the Github repo "${repo}" in the navikt organization - it may not exist, or perhaps it's a private repository?`);
            return;
        }

        const resolvedGithubPath = 'https://maven.pkg.github.com/navikt/' + repo + '/' + path;

        const response = await fetch(resolvedGithubPath, {
            headers: modifiedHeadersWithAuth(req.headers, token),
            credentials: 'same-origin',
            redirect: 'manual'
        });

        if (response.status === 301 || response.status === 200) {
            for (const [key, value] of response.headers) {
                res.header(key, value);
            }
            res.status(response.status);
            response.body.on('end', () => res.end());
            response.body.pipe(res);
        } else if (response.status === 400) {
            res.status(500).send('500 Server error: Could not authenticate with the Github Package Registry. This is probably due to a misconfiguration in Github Package Registry Mirror.');
            console.error('Got status 400 from the server: ' + await response.text());
        } else if (response.status === 404) {
            console.info('Got 404 from Github Package Registry');
            res.status(400).send('404 Not Found: Looks like this package doesn\'t on Github Package Registry.');
        } else if (response.status === 422) {
            res.status(422).send('422: The file path you provided was probably invalid (not a valid Maven repository path)');
        } else {
            res.status(500).send(`Got an unexpected response from Github Package Registry ${resolvedGithubPath}`);
            console.error(`Got unexpected response ${response.status} from Github Package Registry: ` + await response.text());
        }
    } catch (err) {
        console.error('Unexpected error', err);
        res.status(500).send('Server error');
    }
}

function isMavenMetadataXml(path) {
    return path.endsWith("maven-metadata.xml")
}

async function existsInCache(fileName) {
    let exists;
    try {
        console.log(`Checking Cloud Storage for file ${fileName}`);
        let file = bucket().file(fileName);

        exists = (await file.exists())[0];

        if (exists && isMavenMetadataXml(fileName)) {
            const now = new Date();

            const [metadata] = await file.getMetadata();
            let created = new Date(metadata.timeCreated);

            const ageInMilliseconds = now - created;
            const ageInMinutes = ageInMilliseconds / (60 * 1000)

            if (ageInMinutes > 15) {
                console.log(`Too old metadata file ${fileName}, created ${created}, time now ${now}, age ${ageInMinutes} minutes`);
                await file.delete()
                return false
            } else {
                console.log(`Reusing metadata file ${fileName}, created ${created}, time now ${now}, age ${ageInMinutes} minutes`);
                return true
            }
        } else {
            console.log(`Does the file ${fileName} exist?`, exists);
            return exists
        }
    } catch (error) {
        console.error('Could not check if the file existed', error);
        throw error;
    }
}

async function handleCached(req, res, repo, path) {
    try {
        console.log(`Handle cached artifact, repo ${repo}, path ${path}`);
        const file = 'cache/' + repo + '/' + path;

        let exists = await existsInCache(file)

        if (!exists) {
            const token = await getToken('github-token');

            const parsed = parsePathAsArtifact(path);

            console.info(`parsed: ${JSON.stringify(parsed)} , packageName: ${packageName}`);

            if (!parsed.groupId.startsWith("no.nav")) {
                res.status(404).send(`GroupId does not start with 'no.nav'. Assuming a non NAV package`);
                return;
            }

            const isPackagePublicStatus = await isPackagePublic(parsed, token);
            if (!isPackagePublicStatus.result) {
                if (isPackagePublicStatus.error) {
                    console.error(`Could not get package visibility status`, isPackagePublicStatus.error);
                }
                res.status(404).send(`Could not get metadata for the Github repo "${repo}" in the navikt organization - it may not exist, or perhaps it's a private repository?`);
                return;
            }

            const resolvedGithubPath = 'https://maven.pkg.github.com/navikt/' + repo + '/' + path;

            const response = await fetch(resolvedGithubPath, {
                headers: modifiedHeadersWithAuth(req.headers, token),
                credentials: 'same-origin',
                redirect: 'manual'
            });

            console.log(`Fetched from ${resolvedGithubPath}, status code was ${response.status}`);

            if (response.status === 301 || response.status === 302) {
                const location = response.headers.get('location');
                console.log(`Fetching artifact from ${location}`);
                const artifactResponse = await fetch(location);

                if (artifactResponse.status !== 200) {
                    console.error(`artifact response failed with status code ${artifactResponse.status}`);
                    res.status(500).send('Could not fetch the artifact from Github Package Registry.');
                    return;
                }

                const readStream = artifactResponse.body;
                const writeStream = await bucket().file(file).createWriteStream();
                readStream.pipe(writeStream);
                readStream.pipe(res);

                await waitForStreamToEnd(writeStream);
                console.log('stored the response in the bucket.');
                return;
            } else if (response.status === 200) {
                const readStream = response.body;
                const writeStream = await bucket().file(file).createWriteStream();
                readStream.pipe(writeStream);
                readStream.pipe(res);
                await waitForStreamToEnd(writeStream);
                console.log('stored the response in the bucket.');
                return;
            } else if (response.status === 400) {
                res.status(500).send('500 Server error: Could not authenticate with the Github Package Registry. This is probably due to a misconfiguration in Github Package Registry Mirror.');
                console.error('Got status 400 from the server: ' + await response.text());
                return;
            } else if (response.status === 404) {
                console.info('Got 404 from Github Package Registry');
                res.status(404).send('404 Not Found: Looks like this package doesn\'t on Github Package Registry.');
                return;
            } else if (response.status === 422) {
                res.status(422).send('422: The file path you provided was probably invalid (not a valid Maven repository path)');
                return;
            } else {
                res.status(500).send(`Got an unexpected response from Github Package Registry ${resolvedGithubPath}`);
                console.error(`Got unexpected response ${response.status} from Github Package Registry: ` + await response.text());
                return;
            }
        }

        const readStream = await bucket().file(file).createReadStream();
        await readStream.pipe(res);
    } catch (err) {
        console.error('Unexpected error', err);
        res.status(500).send('Server error');
    }
}

const app = express();

app.use(express.static(__dirname));

app.get('/dummy', async (req, res) => {
    try {
        console.log('reading dummy secret');
        const secret = await getToken('dummy-token');
        res.status(200).send(secret);
    } catch (err) {
        console.error('Unexpected error', err);
        res.status(500).send('Server error');
    }
});

app.get('/favicon.ico', (req, res) => res.status(404).end());

app.get('/simple/:repo/*', (req, res) => {
    const repo = req.params.repo;
    const path = req.params[0];
    return handleSimple(req, res, repo, path);
});

app.get('/cached/:repo/*', (req, res) => {
    const repo = req.params.repo;
    const path = req.params[0];
    return handleCached(req, res, repo, path);
});

app.get('/:repo/*', async (req, res) => {
    const repo = req.params.repo;
    const path = req.params[0];
    return handleSimple(req, res, repo, path);
});

const port = 8080;
app.listen(port, () => console.log(`App listening on port ${port}`));

['SIGINT', 'SIGTERM'].forEach(signal => {
    process.once(signal, function () {
        console.log(`${signal} received, exiting.`);
        process.exit(0);
    });
});
