import {CsvModel} from '../src/csv-model.mjs';
self.onmessage=({data})=>{try{const model=new CsvModel(data.text,{delimiter:data.delimiter});self.postMessage({model:{rows:model.rows,delimiter:model.delimiter,newline:model.newline,terminal:model.terminal,bom:model.bom}});}catch(error){self.postMessage({error:error.message});}};
