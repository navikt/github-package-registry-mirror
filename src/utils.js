const fs = require('fs');

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

function isNavPackage(parsed) {
    return parsed.groupId.startsWith("com.github.navikt")
        || parsed.groupId.startsWith("no.nav")
        || parsed.groupId.startsWith("no.stelvio")
}

function isMavenMetadataXml(path) {
    return path.endsWith("maven-metadata.xml")
}

module.exports = { tokenAuthHeader, localFileExists, streamToString, waitForStreamToEnd, modifiedHeadersWithAuth, parsePathAsArtifact, isNavPackage, isMavenMetadataXml };
