// SPDX-License-Identifier: Apache-2.0
import { gunzipSync } from 'fflate';

export type Tree = { name: string; self: number; total: number; children?: Tree[] };
type Field = { number: number; value: bigint | Uint8Array };

// Decode the wire format directly, ignoring fields not needed by the viewer.
// Profiles follow https://github.com/google/pprof/blob/main/proto/profile.proto.
class Reader {
  offset = 0;
  constructor(readonly data: Uint8Array) {}
  varint(): bigint {
    let value = 0n;
    for (let shift = 0n; shift < 70n; shift += 7n) {
      if (this.offset >= this.data.length) throw new Error('Truncated pprof varint');
      const byte = this.data[this.offset++];
      if (shift === 63n && byte > 1) throw new Error('Invalid pprof varint');
      value |= BigInt(byte & 127) << shift;
      if (!(byte & 128)) return value;
    }
    throw new Error('Invalid pprof varint');
  }
  bytes(length: number): Uint8Array {
    if (!Number.isSafeInteger(length) || length < 0 || length > this.data.length - this.offset) {
      throw new Error('Truncated pprof field');
    }
    const result = this.data.subarray(this.offset, this.offset + length);
    this.offset += length;
    return result;
  }
  fields(): Field[] {
    const out: Field[] = [];
    while (this.offset < this.data.length) {
      const key = Number(this.varint());
      const number = Math.floor(key / 8), wire = key & 7;
      if (!number) throw new Error('Invalid pprof field number');
      switch (wire) {
        case 0: out.push({ number, value: this.varint() }); break;
        case 2: out.push({ number, value: this.bytes(Number(this.varint())) }); break;
        case 1: this.bytes(8); break;
        case 5: this.bytes(4); break;
        default: throw new Error('Unsupported pprof wire type');
      }
    }
    return out;
  }
}

function message(value: Field['value']): Field[] {
  if (!(value instanceof Uint8Array)) throw new Error('Expected pprof message');
  return new Reader(value).fields();
}
function scalar(fields: Field[], number: number): bigint {
  const value = fields.find(field => field.number === number)?.value ?? 0n;
  if (typeof value !== 'bigint') throw new Error('Expected pprof integer');
  return value;
}
function repeated(fields: Field[], number: number): bigint[] {
  const out: bigint[] = [];
  for (const field of fields) {
    if (field.number !== number) continue;
    if (typeof field.value === 'bigint') out.push(field.value);
    else {
      const reader = new Reader(field.value);
      while (reader.offset < reader.data.length) out.push(reader.varint());
    }
  }
  return out;
}
function safeNumber(value: bigint): number {
  const number = Number(value);
  if (!Number.isSafeInteger(number)) throw new Error('pprof value exceeds JavaScript integer precision');
  return number;
}

export function parsePprof(input: Uint8Array, sampleType: string): Tree {
  const bytes = input[0] === 0x1f && input[1] === 0x8b ? gunzipSync(input) : input;
  const fields = new Reader(bytes).fields();
  const strings: string[] = [];
  const decoder = new TextDecoder('utf-8', { fatal: true });
  for (const field of fields) if (field.number === 6) {
    if (!(field.value instanceof Uint8Array)) throw new Error('Invalid pprof string table');
    strings.push(decoder.decode(field.value));
  }
  const text = (index: bigint): string => {
    const value = strings[safeNumber(index)];
    if (value === undefined) throw new Error('Invalid pprof string index');
    return value;
  };
  const types = fields.filter(field => field.number === 1).map(field => text(scalar(message(field.value), 1)));
  const index = types.indexOf(sampleType);
  if (index < 0) throw new Error(`Profile has no ${sampleType} samples`);
  const functions = new Map<bigint, string>();
  for (const field of fields) if (field.number === 5) {
    const fn = message(field.value);
    functions.set(scalar(fn, 1), text(scalar(fn, 2)) || 'unknown');
  }
  const locations = new Map<bigint, string[]>();
  for (const field of fields) if (field.number === 4) {
    const location = message(field.value);
    const lines = location.filter(line => line.number === 4).map(line => {
      const id = scalar(message(line.value), 1);
      if (!functions.has(id)) throw new Error('Invalid pprof function reference');
      return functions.get(id)!;
    });
    locations.set(scalar(location, 1), lines);
  }
  const root: Tree = { name: 'all', self: 0, total: 0 };
  const children = new WeakMap<Tree, Map<string, Tree>>();
  const add = (node: Tree, value: number, key: 'self' | 'total') => {
    const sum = node[key] + value;
    if (!Number.isSafeInteger(sum)) throw new Error('pprof total exceeds JavaScript integer precision');
    node[key] = sum;
  };
  for (const field of fields) if (field.number === 2) {
    const sample = message(field.value);
    const values = repeated(sample, 2);
    if (values.length !== types.length) throw new Error('Invalid pprof sample values');
    const value = safeNumber(BigInt.asIntN(64, values[index]));
    if (value <= 0) continue;
    add(root, value, 'total');
    let node = root;
    for (const id of repeated(sample, 1).reverse()) {
      const lines = locations.get(id);
      if (!lines) throw new Error('Invalid pprof location reference');
      for (const name of [...lines].reverse()) {
        let byName = children.get(node);
        if (!byName) { byName = new Map(); children.set(node, byName); }
        let child = byName.get(name);
        if (!child) {
          child = { name, self: 0, total: 0 };
          byName.set(name, child);
          (node.children ??= []).push(child);
        }
        add(child, value, 'total');
        node = child;
      }
    }
    add(node, value, 'self');
  }
  const sort = (node: Tree) => {
    node.children?.sort((a, b) => b.total - a.total);
    node.children?.forEach(sort);
  };
  sort(root);
  return root;
}
