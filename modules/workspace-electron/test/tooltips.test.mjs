import test from 'node:test';
import assert from 'node:assert/strict';
import {tooltipPosition} from '../renderer/tooltips.js';

test('tooltips stay inside the viewport and flip above bottom-edge controls',()=>{
  const viewport={width:800,height:600},size={width:180,height:40};
  assert.deepEqual(tooltipPosition({left:10,width:30,top:10,bottom:40},size,viewport),{left:8,top:48});
  assert.deepEqual(tooltipPosition({left:780,width:20,top:550,bottom:580},size,viewport),{left:612,top:502});
  assert.deepEqual(tooltipPosition({left:300,width:40,top:200,bottom:230},size,viewport),{left:230,top:238});
});
