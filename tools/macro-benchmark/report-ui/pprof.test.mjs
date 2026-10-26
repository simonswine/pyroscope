// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { gzipSync } from 'node:zlib';
import { parsePprof } from './.test/pprof.mjs';

function varint(value) {
  let n = BigInt.asUintN(64, BigInt(value));
  const bytes = [];
  while (n > 127n) { bytes.push(Number(n & 127n) | 128); n >>= 7n; }
  bytes.push(Number(n));
  return Buffer.from(bytes);
}
const scalar = (number, value) => Buffer.concat([varint(number * 8), varint(value)]);
const bytes = (number, value) => Buffer.concat([varint(number * 8 + 2), varint(value.length), value]);
const message = (...fields) => Buffer.concat(fields);
const packed = (number, values) => bytes(number, message(...values.map(varint)));

function fixture({ value = 10, location = 1, packedValues = true, id = 1 } = {}) {
  return message(
    bytes(1, message(scalar(1, 1), scalar(2, 2))),
    bytes(1, message(scalar(1, 3), scalar(2, 4))),
    bytes(2, message(packed(1, [location, 2]), packedValues ? packed(2, [value, 40]) : message(scalar(2, value), scalar(2, 40)))),
    bytes(4, message(scalar(1, 1), bytes(4, scalar(1, id)))),
    bytes(4, message(scalar(1, 2), bytes(4, scalar(1, 2)))),
    bytes(5, message(scalar(1, id), scalar(2, 5))),
    bytes(5, message(scalar(1, 2), scalar(2, 6))),
    ...['', 'cpu', 'nanoseconds', 'alloc_space', 'bytes', 'leaf', 'root'].map(text => bytes(6, Buffer.from(text))),
    scalar(10, 1000000000),
  );
}

const expected = { name: 'all', self: 0, total: 10, children: [
  { name: 'root', self: 0, total: 10, children: [{ name: 'leaf', self: 10, total: 10 }] },
] };

test('decodes raw and gzip-compressed native pprof', () => {
  assert.deepEqual(parsePprof(fixture(), 'cpu'), expected);
  assert.deepEqual(parsePprof(gzipSync(fixture()), 'cpu'), expected);
});
test('selects allocation samples', () => {
  const tree = parsePprof(fixture(), 'alloc_space');
  assert.equal(tree.total, 40);
  assert.equal(tree.children[0].children[0].self, 40);
});
test('supports unpacked sample values and large uint64 identifiers', () => {
  assert.deepEqual(parsePprof(fixture({ packedValues: false, id: 1n << 60n }), 'cpu'), expected);
});
test('skips negative sample values', () => {
  assert.equal(parsePprof(fixture({ value: -1 }), 'cpu').total, 0);
});
test('reports invalid or unsupported profile input', () => {
  assert.throws(() => parsePprof(fixture(), 'missing'), /no missing samples/);
  assert.throws(() => parsePprof(fixture({ location: 999 }), 'cpu'), /location reference/);
  assert.throws(() => parsePprof(fixture({ value: 1n << 60n }), 'cpu'), /integer precision/);
  assert.throws(() => parsePprof(Buffer.from([0]), 'cpu'), /field number/);
  assert.throws(() => parsePprof(Buffer.from([0x80]), 'cpu'), /Truncated/);
  assert.throws(() => parsePprof(fixture().subarray(0, 5), 'cpu'), /Truncated/);
});
