export function canvasPoint(camera, x, y) {
  return {x:(x-camera.x)/camera.zoom, y:(y-camera.y)/camera.zoom};
}

export function canvasZoomAt(camera, zoom, x, y) {
  if (![zoom,x,y].every(Number.isFinite)) return {...camera};
  const point = canvasPoint(camera,x,y), next = Math.max(.00001,Math.min(4,zoom));
  return {zoom:next, x:x-point.x*next, y:y-point.y*next};
}

export function canvasFit(bounds, width, height, padding = 48) {
  if (!bounds) return {x:width/2, y:height/2, zoom:1};
  const zoom = Math.max(.00001,Math.min(1,Math.max(1,width-padding*2)/bounds.width,
    Math.max(1,height-padding*2)/bounds.height));
  return {zoom, x:width/2-(bounds.x+bounds.width/2)*zoom,
    y:height/2-(bounds.y+bounds.height/2)*zoom};
}

export function canvasWheelZoom(camera, event, height) {
  const unit = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? height : 1;
  return canvasZoomAt(camera,camera.zoom*Math.exp(-Math.max(-400,Math.min(400,event.deltaY*unit))*.002),event.x,event.y);
}
