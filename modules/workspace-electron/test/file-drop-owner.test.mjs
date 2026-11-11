import test from 'node:test';
import assert from 'node:assert/strict';
import {fileDropEffect} from '../renderer/file-tree.js';
test('Mac drag moves only within Mac, and copies explicitly to Main Box',()=>{
 assert.equal(fileDropEffect('mac','mac'),'move');assert.equal(fileDropEffect('mac','box'),'copy');
 assert.equal(fileDropEffect('mac','storage'),null);assert.equal(fileDropEffect('box','mac'),null);
 assert.equal(fileDropEffect('box','storage'),'move');assert.equal(fileDropEffect('storage','box'),'move');
 assert.equal(fileDropEffect('appdata_test','mac'),null);
});
