'use strict';
const fs = require('fs');
const path = require('path');

function createGCSStorage(bucketName) {
    const { Storage } = require('@google-cloud/storage');
    const bucket = new Storage().bucket(bucketName);
    return {
        file(fileName) {
            return bucket.file(fileName);
        }
    };
}

function createLocalStorage(basePath) {
    return {
        file(fileName) {
            const filePath = path.join(basePath, fileName);
            return {
                exists() {
                    return fs.promises.access(filePath)
                        .then(() => [true])
                        .catch(() => [false]);
                },
                getMetadata() {
                    return fs.promises.stat(filePath)
                        .then(stat => [{ timeCreated: stat.mtime.toISOString() }]);
                },
                createReadStream() {
                    return fs.createReadStream(filePath);
                },
                createWriteStream() {
                    fs.mkdirSync(path.dirname(filePath), { recursive: true });
                    return fs.createWriteStream(filePath);
                },
                delete() {
                    return fs.promises.unlink(filePath);
                }
            };
        }
    };
}

module.exports = { createGCSStorage, createLocalStorage };
