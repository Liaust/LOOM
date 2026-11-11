export function sidebarBounds(windowWidth,detailsWidth=0){return {min:166,max:Math.max(166,Math.min(Math.floor(windowWidth*.75),windowWidth-44-detailsWidth-180))};}
export function sidebarWidth(value,windowWidth,detailsWidth=0){const {min,max}=sidebarBounds(windowWidth,detailsWidth);return Math.min(max,Math.max(min,Number.isFinite(Number(value))?Number(value):246));}
