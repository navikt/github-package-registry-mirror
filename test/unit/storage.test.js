'use strict';
const { describe, it, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { createLocalStorage } = require('../../src/storage');

function makeTmpDir() {
    return path.join(os.tmpdir(), 'test-storage-' + Date.now() + '-' + Math.random().toString(36).slice(2));
}

function writeStream(stream, content) {
    return new Promise((resolve, reject) => {
        stream.on('finish', resolve);
        stream.on('error', reject);
        stream.write(content);
        stream.end();
    });
}

function readStream(stream) {
    return new Promise((resolve, reject) => {
        let data = '';
        stream.on('data', chunk => { data += chunk; });
        stream.on('end', () => resolve(data));
        stream.on('error', reject);
    });
}

describe('createLocalStorage', () => {
    const dirs = [];

    after(() => {
        for (const dir of dirs) {
            fs.rmSync(dir, { recursive: true, force: true });
        }
    });

    it('write then read round-trip', async () => {
        const dir = makeTmpDir();
        dirs.push(dir);
        const s = createLocalStorage(dir);
        await writeStream(s.file('artifact.jar').createWriteStream(), 'hello world');
        const content = await readStream(s.file('artifact.jar').createReadStream());
        assert.equal(content, 'hello world');
    });

    it('exists() returns [true] for existing file', async () => {
        const dir = makeTmpDir();
        dirs.push(dir);
        const s = createLocalStorage(dir);
        await writeStream(s.file('present.txt').createWriteStream(), 'data');
        const [exists] = await s.file('present.txt').exists();
        assert.equal(exists, true);
    });

    it('exists() returns [false] for missing file', async () => {
        const dir = makeTmpDir();
        dirs.push(dir);
        const s = createLocalStorage(dir);
        const [exists] = await s.file('missing.txt').exists();
        assert.equal(exists, false);
    });

    it('getMetadata() returns timeCreated as valid ISO string', async () => {
        const dir = makeTmpDir();
        dirs.push(dir);
        const s = createLocalStorage(dir);
        await writeStream(s.file('meta.txt').createWriteStream(), 'data');
        const [meta] = await s.file('meta.txt').getMetadata();
        assert.equal(typeof meta.timeCreated, 'string');
        assert.ok(!isNaN(new Date(meta.timeCreated).getTime()), 'timeCreated should be a valid date');
    });

    it('delete() removes the file', async () => {
        const dir = makeTmpDir();
        dirs.push(dir);
        const s = createLocalStorage(dir);
        await writeStream(s.file('to-delete.txt').createWriteStream(), 'bye');
        await s.file('to-delete.txt').delete();
        const [exists] = await s.file('to-delete.txt').exists();
        assert.equal(exists, false);
    });

    it('createWriteStream creates nested directories', async () => {
        const dir = makeTmpDir();
        dirs.push(dir);
        const s = createLocalStorage(dir);
        await writeStream(s.file('deep/nested/path/file.txt').createWriteStream(), 'nested');
        const [exists] = await s.file('deep/nested/path/file.txt').exists();
        assert.equal(exists, true);
    });

    it('createReadStream on missing file emits error event', async () => {
        const dir = makeTmpDir();
        dirs.push(dir);
        const s = createLocalStorage(dir);
        const stream = s.file('nonexistent.txt').createReadStream();
        await new Promise((resolve, reject) => {
            stream.on('error', () => resolve());
            stream.on('end', () => reject(new Error('Expected error but got end')));
            stream.resume(); // trigger read
        });
    });
});
