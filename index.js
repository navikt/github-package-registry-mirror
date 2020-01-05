const express = require('express');
const fetch = require('node-fetch');
const { Storage } = require('@google-cloud/storage');

function tokenAuthHeader(token) {
    return 'Basic ' + Buffer.from("token:" + token).toString('base64');
}

async function getRepoStatus(name, token) {
    const repo = await fetch('https://api.github.com/repos/navikt/' + encodeURIComponent(name), {
        headers: {
            authorization: tokenAuthHeader(token)
        }
    });
    const data = await repo.json();
    if (repo.status === 404) {
        return {
            error: 'REPO_NOT_FOUND'
        };
    }
    if (data.private) {
        return {
            error: 'REPO_IS_PRIVATE'
        };
    }
    return {
        message: 'OK'
    };
}

function streamToString (stream) {
    const chunks = [];
    return new Promise((resolve, reject) => {
        stream.on('data', chunk => chunks.push(chunk));
        stream.on('error', reject);
        stream.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')));
    });
}

const app = express();

app.get('/', (req, res) => {
    res.send('Dette er et mirror for Github Package Registry. Work in progress...');
});

const storage = new Storage();
async function getToken(tokenName) {
    const stream = await storage.bucket('github-package-registry-mirror-storage').file('credentials/' + tokenName).createReadStream();
    const data = await streamToString(stream);
    return data.trim();
}

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

app.get('/:repo/*', async (req, res) => {
    try {
        const token = await getToken('github-token');

        const repoStatus = await getRepoStatus(req.params.repo, token);
        if (repoStatus.error) {
            console.error('Could not read repo metadata', repoStatus.error);
            res.status(404).send(`Kunne ikke hente metadata for Github-repoet "${req.params.repo}" under navikt-organisasjonen - det kan hende det ikke finnes, eller at det er privat?`);
            return;
        }

        const modifiedHeaders = {
            ...req.headers,
            authorization: tokenAuthHeader(token)
        };
        delete modifiedHeaders.host;

        const resolvedGithubPath = 'https://maven.pkg.github.com/navikt' + req.originalUrl;

        const response = await fetch(resolvedGithubPath, {
            headers: modifiedHeaders,
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
            res.status(500).send('500 Server error: Could not authenticate with the Github Package Registry. This is probably due to a misconfiguration in Github Package Registry Mirror, and not your fault.');
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
});

const port = 8080;
app.listen(port, () => console.log(`App listening on port ${port}`));

['SIGINT', 'SIGTERM'].forEach(signal => {
    process.once(signal, function () {
        console.log(`${signal} received, exiting.`);
        process.exit(0);
    });
});
