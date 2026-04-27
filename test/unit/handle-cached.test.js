'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const http = require('http');
const { Readable, Writable } = require('stream');

const { createApp } = require('../../src/app');

async function makeServer(fetchFn, getTokenFn, storageFn) {
    const app = createApp({ fetchFn, getTokenFn, storageFn });
    const server = http.createServer(app);
    await new Promise((resolve) => server.listen(0, resolve));
    return server;
}

function makeStorage({ exists = false, timeCreated = null, content = 'cached-content' } = {}) {
    let writtenData = '';
    let deleteCalled = false;

    return {
        file: () => ({
            exists: async () => [exists],
            getMetadata: async () => [{ timeCreated: timeCreated || new Date().toISOString() }],
            createReadStream: () => Readable.from([content]),
            createWriteStream: () => new Writable({
                write(chunk, enc, cb) {
                    writtenData += chunk.toString();
                    cb();
                }
            }),
            delete: async () => {
                deleteCalled = true;
            },
        }),
        getWrittenData: () => writtenData,
        wasDeleted: () => deleteCalled,
    };
}

function publicGraphQL() {
    return {
        status: 200,
        json: async () => ({
            data: {
                organization: {
                    packages: {
                        nodes: [{ repository: { isPrivate: false } }]
                    }
                }
            }
        }),
        headers: new Map(),
        text: async () => ''
    };
}

function makeArtifactResponse(status, body = 'artifact-bytes') {
    return {
        status,
        headers: new Map([
            ['content-type', 'application/octet-stream'],
            ['location', 'https://objects.githubusercontent.com/artifact']]
        ),
        body: Readable.from([body]),
        text: async () => body,
    };
}

function makeFetchSequence(responses) {
    const calls = [];
    const fetchFn = async (url, options = undefined) => {
        calls.push({ url, options });
        const next = responses.shift();
        if (!next) {
            throw new Error(`Unexpected fetch call for ${url}`);
        }
        return next;
    };

    fetchFn.calls = calls;
    return fetchFn;
}

function request(server, path) {
    return new Promise((resolve, reject) => {
        const { port } = server.address();
        const req = http.get(`http://127.0.0.1:${port}${path}`, (res) => {
            let body = '';
            res.setEncoding('utf8');
            res.on('data', (chunk) => {
                body += chunk;
            });
            res.on('end', () => resolve({ status: res.statusCode, body, headers: res.headers }));
        });
        req.on('error', reject);
    });
}

async function closeServer(server) {
    await new Promise((resolve, reject) => server.close((err) => err ? reject(err) : resolve()));
}

const normalPath = '/cached/tjenestespesifikasjoner/no/nav/foo/bar/1.0/bar-1.0.jar';
const metadataPath = '/cached/tjenestespesifikasjoner/no/nav/foo/bar/maven-metadata.xml';
const nonNavPath = '/cached/commons-lang/org/apache/commons/lang3/3.0/lang3-3.0.jar';

test('cache miss upstream 200 returns 200 and stores artifact', async (t) => {
    const storage = makeStorage({ exists: false });
    const fetchFn = makeFetchSequence([publicGraphQL(), makeArtifactResponse(200)]);
    const server = await makeServer(fetchFn, async () => 'token', storage);
    t.after(() => closeServer(server));

    const response = await request(server, normalPath);

    assert.equal(response.status, 200);
    assert.equal(response.body, 'artifact-bytes');
    assert.equal(storage.getWrittenData(), 'artifact-bytes');
    assert.equal(fetchFn.calls.length, 2);
});

test('cache miss upstream 301 redirect follows target and returns 200', async (t) => {
    const storage = makeStorage({ exists: false });
    const fetchFn = makeFetchSequence([
        publicGraphQL(),
        makeArtifactResponse(301),
        makeArtifactResponse(200, 'redirected-301-body')
    ]);
    const server = await makeServer(fetchFn, async () => 'token', storage);
    t.after(() => closeServer(server));

    const response = await request(server, normalPath);

    assert.equal(response.status, 200);
    assert.equal(response.body, 'redirected-301-body');
    assert.equal(storage.getWrittenData(), 'redirected-301-body');
    assert.equal(fetchFn.calls.length, 3);
});

test('cache miss upstream 302 redirect follows target and returns 200', async (t) => {
    const storage = makeStorage({ exists: false });
    const fetchFn = makeFetchSequence([
        publicGraphQL(),
        makeArtifactResponse(302),
        makeArtifactResponse(200, 'redirected-302-body')
    ]);
    const server = await makeServer(fetchFn, async () => 'token', storage);
    t.after(() => closeServer(server));

    const response = await request(server, normalPath);

    assert.equal(response.status, 200);
    assert.equal(response.body, 'redirected-302-body');
    assert.equal(storage.getWrittenData(), 'redirected-302-body');
    assert.equal(fetchFn.calls.length, 3);
});

test('cache miss redirect target failure returns 500', async (t) => {
    const storage = makeStorage({ exists: false });
    const fetchFn = makeFetchSequence([
        publicGraphQL(),
        makeArtifactResponse(302),
        makeArtifactResponse(500, 'redirect-target-failed')
    ]);
    const server = await makeServer(fetchFn, async () => 'token', storage);
    t.after(() => closeServer(server));

    const response = await request(server, normalPath);

    assert.equal(response.status, 500);
    assert.match(response.body, /Could not fetch the artifact/);
});

test('cache hit serves from storage without GitHub fetches', async (t) => {
    const storage = makeStorage({ exists: true, content: 'cached-hit-body' });
    const fetchFn = makeFetchSequence([]);
    const server = await makeServer(fetchFn, async () => 'token', storage);
    t.after(() => closeServer(server));

    const response = await request(server, normalPath);

    assert.equal(response.status, 200);
    assert.equal(response.body, 'cached-hit-body');
    assert.equal(fetchFn.calls.length, 0);
});

test('cache hit maven-metadata.xml fresh TTL under 15 minutes serves cached metadata', async (t) => {
    const fiveMinutesAgo = new Date(Date.now() - 5 * 60 * 1000).toISOString();
    const storage = makeStorage({ exists: true, timeCreated: fiveMinutesAgo, content: 'fresh-metadata-body' });
    const fetchFn = makeFetchSequence([]);
    const server = await makeServer(fetchFn, async () => 'token', storage);
    t.after(() => closeServer(server));

    const response = await request(server, metadataPath);

    assert.equal(response.status, 200);
    assert.equal(response.body, 'fresh-metadata-body');
    assert.equal(fetchFn.calls.length, 0);
    assert.equal(storage.wasDeleted(), false);
});

test('cache hit maven-metadata.xml stale TTL over 15 minutes deletes stale metadata and refetches', async (t) => {
    const twentyMinutesAgo = new Date(Date.now() - 20 * 60 * 1000).toISOString();
    const storage = makeStorage({ exists: true, timeCreated: twentyMinutesAgo });
    const fetchFn = makeFetchSequence([
        publicGraphQL(),
        makeArtifactResponse(200, 'refetched-metadata-body')
    ]);
    const server = await makeServer(fetchFn, async () => 'token', storage);
    t.after(() => closeServer(server));

    const response = await request(server, metadataPath);

    // stale metadata TTL: > 15 minutes triggers delete + refetch
    assert.equal(response.status, 200);
    assert.equal(response.body, 'refetched-metadata-body');
    assert.equal(storage.wasDeleted(), true);
    assert.equal(storage.getWrittenData(), 'refetched-metadata-body');
    assert.equal(fetchFn.calls.length, 2);
});

test('non-NAV package returns 404', async (t) => {
    const storage = makeStorage({ exists: false });
    const fetchFn = makeFetchSequence([]);
    const server = await makeServer(fetchFn, async () => 'token', storage);
    t.after(() => closeServer(server));

    const response = await request(server, nonNavPath);

    assert.equal(response.status, 404);
    assert.match(response.body, /non NAV package/);
});

test('upstream 404 maps to 404 in handleCached', async (t) => {
    const storage = makeStorage({ exists: false });
    const fetchFn = makeFetchSequence([
        publicGraphQL(),
        makeArtifactResponse(404, 'missing-upstream-artifact')
    ]);
    const server = await makeServer(fetchFn, async () => 'token', storage);
    t.after(() => closeServer(server));

    const response = await request(server, normalPath);

    // handleCached correctly returns 404 (unlike handleSimple which returns 400)
    assert.equal(response.status, 404);
    assert.match(response.body, /404 Not Found/);
});

test('exception from getTokenFn returns 500', async (t) => {
    const storage = makeStorage({ exists: false });
    const fetchFn = makeFetchSequence([]);
    const server = await makeServer(fetchFn, async () => {
        throw new Error('boom');
    }, storage);
    t.after(() => closeServer(server));

    const response = await request(server, normalPath);

    assert.equal(response.status, 500);
    assert.equal(response.body, 'Server error');
});
