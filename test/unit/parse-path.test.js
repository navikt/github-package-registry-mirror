const test = require('node:test');
const assert = require('node:assert/strict');

const { parsePathAsArtifact } = require('../../src/utils');

test('parses a standard jar artifact path', () => {
  assert.deepStrictEqual(
    parsePathAsArtifact('no/nav/foo/bar/1.0.0/bar-1.0.0.jar'),
    {
      groupId: 'no.nav.foo',
      artifactId: 'bar',
      version: '1.0.0',
      file: 'bar-1.0.0.jar',
    }
  );
});

test('parses a standard pom artifact path', () => {
  assert.deepStrictEqual(
    parsePathAsArtifact('com/github/navikt/mylib/2.3.4/mylib-2.3.4.pom'),
    {
      groupId: 'com.github.navikt',
      artifactId: 'mylib',
      version: '2.3.4',
      file: 'mylib-2.3.4.pom',
    }
  );
});

test('parses a deep groupId artifact path', () => {
  assert.deepStrictEqual(
    parsePathAsArtifact(
      'no/nav/tjenestespesifikasjoner/aktorid-jaxws/1.2019.12.18-12.22-ce897c4eb2c1/aktorid-jaxws-1.2019.12.18-12.22-ce897c4eb2c1.pom'
    ),
    {
      groupId: 'no.nav.tjenestespesifikasjoner',
      artifactId: 'aktorid-jaxws',
      version: '1.2019.12.18-12.22-ce897c4eb2c1',
      file: 'aktorid-jaxws-1.2019.12.18-12.22-ce897c4eb2c1.pom',
    }
  );
});

test('parses maven-metadata.xml paths with empty version', () => {
  assert.deepStrictEqual(parsePathAsArtifact('no/nav/foo/bar/maven-metadata.xml'), {
    groupId: 'no.nav.foo',
    artifactId: 'bar',
    version: '',
    file: 'maven-metadata.xml',
  });
});

test('throws for paths shorter than four characters', () => {
  assert.throws(() => parsePathAsArtifact('a/b'), /not a valid Maven repository path/);
});

test('throws for the three-character path abc', () => {
  assert.throws(() => parsePathAsArtifact('abc'), /not a valid Maven repository path/);
});

test('does not throw for length-4 paths even if they are not valid repository paths', () => {
  // Known quirk: checks string length, not segment count
  assert.doesNotThrow(() => parsePathAsArtifact('abcd'));
  assert.deepStrictEqual(parsePathAsArtifact('abcd'), {
    groupId: '',
    artifactId: undefined,
    version: undefined,
    file: 'abcd',
  });
});

test('does not throw for single-segment paths longer than four characters', () => {
  assert.doesNotThrow(() => parsePathAsArtifact('abcdef'));
  assert.deepStrictEqual(parsePathAsArtifact('abcdef'), {
    groupId: '',
    artifactId: undefined,
    version: undefined,
    file: 'abcdef',
  });
});
