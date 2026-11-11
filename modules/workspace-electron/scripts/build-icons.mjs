import {execFileSync} from 'node:child_process';
import {cp,mkdir} from 'node:fs/promises';
import path from 'node:path';

export async function buildIcons(source,output){
  await mkdir(output,{recursive:true});
  const base=path.join(output,'Loom');
  await cp(source,`${base}.icon`,{recursive:true,errorOnExist:true,force:false});
  const developer=execFileSync('xcode-select',['-p'],{encoding:'utf8'}).trim();
  const tool=path.resolve(developer,'../Applications/Icon Composer.app/Contents/Executables/ictool');
  const image=path.join(output,'Loom.png'),iconset=`${base}.iconset`;
  execFileSync(tool,[source,'--export-image','--output-file',image,'--platform','macOS','--rendition','Default','--width','1024','--height','1024','--scale','1'],{stdio:'inherit'});
  await mkdir(iconset);
  for(const size of [16,32,128,256,512])for(const scale of [1,2]){
    execFileSync('/usr/bin/sips',['-z',String(size*scale),String(size*scale),image,'--out',path.join(iconset,`icon_${size}x${size}${scale===2?'@2x':''}.png`)],{stdio:'ignore'});
  }
  execFileSync('/usr/bin/iconutil',['-c','icns',iconset,'-o',`${base}.icns`],{stdio:'inherit'});
  return base;
}
