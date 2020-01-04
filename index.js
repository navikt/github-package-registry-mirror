const express = require('express');
const fetch = require('node-fetch');
const { Storage } = require('@google-cloud/storage');

// Creates a Cloud Storage client
const storage = new Storage();

const app = express();

app.get('/', (req, res) => {
    res.send('Dette er et mirror for Github Package Registry. Work in progress...');
});

function streamToString (stream) {
    const chunks = []
    return new Promise((resolve, reject) => {
        stream.on('data', chunk => chunks.push(chunk))
        stream.on('error', reject)
        stream.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')))
    })
}


app.get('/dummy', async (req, res) => {
    try {
        console.log('reading dummy secret');
        const stream = await storage.bucket('github-package-registry-mirror-storage').file('credentials/dummy-token').createReadStream();
        const file = await streamToString(stream);
        res.status(200).send(JSON.stringify(file));
    } catch (err) {
        console.error('Unexpected error', err);
        res.status(500).send('Server error');
    }
});

app.get('/favicon.ico', (req, res) => res.status(404).end());

app.get('*', async (req, res) => {
    try {
        if (!req.originalUrl.startsWith('/tjenestespesifikasjoner/')) {
            res.status(400).send('Ugyldig pakke: kun tjenestespesifikasjoner er whitelistet til bruk i mirroret hittil.');
            return;
        }
        const resolved = 'https://maven.pkg.github.com/navikt' + req.originalUrl;

        const username = 'token';
        const password = process.env.GITHUB_TOKEN;

        const modifiedHeaders = {
            ...req.headers,
            authorization: 'Basic ' + Buffer.from(username + ":" + password).toString('base64')
        };
        delete modifiedHeaders.host;

        const response = await fetch(resolved, {
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
            res.status(500).send(`Got an unexpected response from Github Package Registry ${resolved}`);
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
