'use strict';
const test = require('node:test');
const { afterEach } = require('node:test');
const assert = require('node:assert/strict');
const http = require('http');
const { once } = require('node:events');
const { Readable } = require('stream');

const { createApp } = require('../../src/app');
const { tokenAuthHeader } = require('../../src/utils');

let server;

function makeServer(fetchFn, getTokenFn) {
    const mockStorage = { file: () => ({ exists: async () => [false] }) };
    const app = createApp({ fetchFn, getTokenFn, storageFn: mockStorage });
    const instance = http.createServer(app);
    instance.listen(0);
    return instance;
}

function makeGraphQlResponse(isPrivate) {
    return {
        status: 200,
        json: async () => ({
            data: {
                organization: {
                    packages: {
                        nodes: [{ repository: { isPrivate } }],
                    },
                },
            },
        }),
        headers: new Map(),
        text: async () => '',
    };
}

function makePackageNotFoundResponse() {
    return {
        status: 200,
        json: async () => ({
            data: {
                organization: {
                    packages: {
                        nodes: [],
                    },
                },
            },
        }),
        headers: new Map(),
        text: async () => '',
    };
}

function makeArtifactResponse(status, body = '', extraHeaders = []) {
    const bodyStream = Readable.from([body]);
    const headers = new Map([
        ['content-type', 'application/octet-stream'],
        ...extraHeaders,
    ]);
    return { status, headers, body: bodyStream, text: async () => body };
}

async function closeServer(instance) {
    if (!instance) {
        return;
    }

    await new Promise((resolve, reject) => {
        instance.close((error) => {
            if (error) {
                reject(error);
                return;
            }
            resolve();
        });
    });
}

async function request(serverInstance, path, options = {}) {
    if (!serverInstance.listening) {
        await once(serverInstance, 'listening');
    }

    const { redirect = 'follow' } = options;
    const port = serverInstance.address().port;
    return fetch(`http://localhost:${port}${path}`, { redirect });
}

afterEach(async () => {
    await closeServer(server);
    server = undefined;
});

test('handleSimple returns 200 and streams artifact body for public packages', async () => {
    const calls = [];
    const token = 'secret-token';
    const fetchFn = async (url, options = {}) => {
        calls.push({ url, options });
        if (url.includes('api.github.com/graphql')) {
            assert.match(options.headers.authorization, /^bearer /);
            assert.strictEqual(options.headers.authorization, `bearer ${token}`);
            return makeGraphQlResponse(false);
        }

        if (url.includes('maven.pkg.github.com')) {
            assert.strictEqual(options.headers.authorization, tokenAuthHeader(token));
            return makeArtifactResponse(200, 'artifact-data');
        }

        throw new Error(`Unexpected URL ${url}`);
    };

    server = makeServer(fetchFn, async () => token);
    const response = await request(server, '/simple/tjenestespesifikasjoner/no/nav/foo/bar/1.0/bar-1.0.jar');

    assert.strictEqual(response.status, 200);
    assert.strictEqual(await response.text(), 'artifact-data');
    assert.strictEqual(calls.length, 2);
});

test('handleSimple forwards 301 redirect responses from the artifact endpoint', async () => {
    const token = 'secret-token';
    const fetchFn = async (url, options = {}) => {
        if (url.includes('api.github.com/graphql')) {
            assert.strictEqual(options.headers.authorization, `bearer ${token}`);
            return makeGraphQlResponse(false);
        }

        if (url.includes('maven.pkg.github.com')) {
            assert.strictEqual(options.headers.authorization, tokenAuthHeader(token));
            return makeArtifactResponse(301, '', [['location', 'https://example.test/artifact.jar']]);
        }

        throw new Error(`Unexpected URL ${url}`);
    };

    server = makeServer(fetchFn, async () => token);
    const response = await request(
        server,
        '/simple/tjenestespesifikasjoner/no/nav/foo/bar/1.0/bar-1.0.jar',
        { redirect: 'manual' }
    );

    assert.strictEqual(response.status, 301);
    assert.strictEqual(response.headers.get('location'), 'https://example.test/artifact.jar');
});

test('handleSimple returns 404 for non-NAV packages', async () => {
    let fetchCalls = 0;
    server = makeServer(async () => {
        fetchCalls += 1;
        throw new Error('fetch should not be called for non-NAV packages');
    }, async () => 'secret-token');

    const response = await request(
        server,
        '/simple/commons-lang/org/apache/commons/lang3/3.0/lang3-3.0.jar'
    );

    assert.strictEqual(response.status, 404);
    assert.match(await response.text(), /accepted prefix/);
    assert.strictEqual(fetchCalls, 0);
});

test('handleSimple returns 404 for private packages', async () => {
    let artifactCalled = false;
    const fetchFn = async (url) => {
        if (url.includes('api.github.com/graphql')) {
            return makeGraphQlResponse(true);
        }

        if (url.includes('maven.pkg.github.com')) {
            artifactCalled = true;
            return makeArtifactResponse(200, 'artifact-data');
        }

        throw new Error(`Unexpected URL ${url}`);
    };

    server = makeServer(fetchFn, async () => 'secret-token');
    const response = await request(server, '/simple/tjenestespesifikasjoner/no/nav/foo/bar/1.0/bar-1.0.jar');

    assert.strictEqual(response.status, 404);
    assert.match(await response.text(), /private repository/);
    assert.strictEqual(artifactCalled, false);
});

test('handleSimple returns 404 when package metadata is not found', async () => {
    let artifactCalled = false;
    const fetchFn = async (url) => {
        if (url.includes('api.github.com/graphql')) {
            return makePackageNotFoundResponse();
        }

        if (url.includes('maven.pkg.github.com')) {
            artifactCalled = true;
            return makeArtifactResponse(200, 'artifact-data');
        }

        throw new Error(`Unexpected URL ${url}`);
    };

    server = makeServer(fetchFn, async () => 'secret-token');
    const response = await request(server, '/simple/tjenestespesifikasjoner/no/nav/foo/bar/1.0/bar-1.0.jar');

    assert.strictEqual(response.status, 404);
    assert.match(await response.text(), /may not exist/);
    assert.strictEqual(artifactCalled, false);
});

test('handleSimple maps upstream 400 to client 500', async () => {
    const fetchFn = async (url) => {
        if (url.includes('api.github.com/graphql')) {
            return makeGraphQlResponse(false);
        }

        if (url.includes('maven.pkg.github.com')) {
            return makeArtifactResponse(400, 'bad credentials');
        }

        throw new Error(`Unexpected URL ${url}`);
    };

    server = makeServer(fetchFn, async () => 'secret-token');
    const response = await request(server, '/simple/tjenestespesifikasjoner/no/nav/foo/bar/1.0/bar-1.0.jar');

    assert.strictEqual(response.status, 500);
    assert.match(await response.text(), /Could not authenticate/);
});

test('handleSimple maps upstream 404 to client 400', async () => {
    const fetchFn = async (url) => {
        if (url.includes('api.github.com/graphql')) {
            return makeGraphQlResponse(false);
        }

        if (url.includes('maven.pkg.github.com')) {
            return makeArtifactResponse(404, 'missing artifact');
        }

        throw new Error(`Unexpected URL ${url}`);
    };

    server = makeServer(fetchFn, async () => 'secret-token');
    const response = await request(server, '/simple/tjenestespesifikasjoner/no/nav/foo/bar/1.0/bar-1.0.jar');

    // Known inconsistency: handleSimple maps upstream 404 to client 400
    assert.strictEqual(response.status, 400);
    assert.match(await response.text(), /Looks like this package/);
});

test('handleSimple preserves upstream 422 responses', async () => {
    const fetchFn = async (url) => {
        if (url.includes('api.github.com/graphql')) {
            return makeGraphQlResponse(false);
        }

        if (url.includes('maven.pkg.github.com')) {
            return makeArtifactResponse(422, 'invalid path');
        }

        throw new Error(`Unexpected URL ${url}`);
    };

    server = makeServer(fetchFn, async () => 'secret-token');
    const response = await request(server, '/simple/tjenestespesifikasjoner/no/nav/foo/bar/1.0/bar-1.0.jar');

    assert.strictEqual(response.status, 422);
    assert.match(await response.text(), /not a valid Maven repository path/);
});

test('handleSimple maps unexpected upstream statuses to client 500', async () => {
    const fetchFn = async (url) => {
        if (url.includes('api.github.com/graphql')) {
            return makeGraphQlResponse(false);
        }

        if (url.includes('maven.pkg.github.com')) {
            return makeArtifactResponse(429, 'rate limited');
        }

        throw new Error(`Unexpected URL ${url}`);
    };

    server = makeServer(fetchFn, async () => 'secret-token');
    const response = await request(server, '/simple/tjenestespesifikasjoner/no/nav/foo/bar/1.0/bar-1.0.jar');

    assert.strictEqual(response.status, 500);
    assert.match(await response.text(), /Got an unexpected response/);
});

test('handleSimple returns 500 when token lookup throws', async () => {
    let fetchCalls = 0;
    server = makeServer(async () => {
        fetchCalls += 1;
        return makeGraphQlResponse(false);
    }, async () => {
        throw new Error('boom');
    });

    const response = await request(server, '/simple/tjenestespesifikasjoner/no/nav/foo/bar/1.0/bar-1.0.jar');

    assert.strictEqual(response.status, 500);
    assert.strictEqual(await response.text(), 'Server error');
    assert.strictEqual(fetchCalls, 0);
});
