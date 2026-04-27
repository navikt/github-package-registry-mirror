const test = require('node:test');
const assert = require('node:assert/strict');

const {
  tokenAuthHeader,
  isNavPackage,
  isMavenMetadataXml,
  modifiedHeadersWithAuth,
} = require('../../src/utils');

test('tokenAuthHeader builds basic auth header from token', () => {
  const token = 'mytoken';
  assert.strictEqual(
    tokenAuthHeader(token),
    `Basic ${Buffer.from(`token:${token}`).toString('base64')}`
  );
});

test('tokenAuthHeader handles empty token without crashing', () => {
  assert.strictEqual(
    tokenAuthHeader(''),
    `Basic ${Buffer.from('token:').toString('base64')}`
  );
});

test('isNavPackage returns true for com.github.navikt groupId', () => {
  assert.strictEqual(isNavPackage({ groupId: 'com.github.navikt.something' }), true);
});

test('isNavPackage returns true for no.nav groupId', () => {
  assert.strictEqual(isNavPackage({ groupId: 'no.nav.foo' }), true);
});

test('isNavPackage returns true for no.stelvio groupId', () => {
  assert.strictEqual(isNavPackage({ groupId: 'no.stelvio.bar' }), true);
});

test('isNavPackage returns false for non-NAV groupId', () => {
  assert.strictEqual(isNavPackage({ groupId: 'org.apache.commons' }), false);
});

test('isNavPackage returns false for other github groupId', () => {
  assert.strictEqual(isNavPackage({ groupId: 'com.github.other' }), false);
});

test('isNavPackage returns false for empty groupId', () => {
  assert.strictEqual(isNavPackage({ groupId: '' }), false);
});

test('isMavenMetadataXml returns true for nested maven metadata path', () => {
  assert.strictEqual(isMavenMetadataXml('com/example/foo/maven-metadata.xml'), true);
});

test('isMavenMetadataXml returns true for root maven metadata path', () => {
  assert.strictEqual(isMavenMetadataXml('maven-metadata.xml'), true);
});

test('isMavenMetadataXml returns false for artifact jar path', () => {
  assert.strictEqual(isMavenMetadataXml('com/example/foo/artifact-1.0.jar'), false);
});

test('isMavenMetadataXml returns false for checksum file', () => {
  assert.strictEqual(isMavenMetadataXml('maven-metadata.xml.sha1'), false);
});

test('isMavenMetadataXml returns false for similar suffix', () => {
  assert.strictEqual(isMavenMetadataXml('path/to/maven-metadata.xmlx'), false);
});

test('modifiedHeadersWithAuth sets authorization header and removes host', () => {
  const headers = { host: 'example.com', accept: 'application/json' };
  const token = 'mytoken';

  assert.deepStrictEqual(modifiedHeadersWithAuth(headers, token), {
    accept: 'application/json',
    authorization: tokenAuthHeader(token),
  });
});

test('modifiedHeadersWithAuth does not mutate original headers object', () => {
  const headers = { host: 'example.com', accept: 'application/json' };
  const copy = { ...headers };

  modifiedHeadersWithAuth(headers, 'mytoken');

  assert.deepStrictEqual(headers, copy);
});
