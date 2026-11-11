export function scheduleTiming(input){
  const cadence=input.cadence||'advanced';
  if(cadence==='once'){
    // datetime-local belongs to the displayed client timezone; persist an
    // unambiguous instant, not a naive string interpreted on another machine.
    const at=new Date(input.at);if(!Number.isFinite(+at)||+at<=Date.now())throw Error('Choose a future date and time.');
    return {kind:'one_shot',expression:at.toISOString(),timezone:'UTC'};
  }
  if(cadence==='advanced')return {kind:'cron',expression:input.expression,timezone:input.timezone};
  if(!['daily','weekly'].includes(cadence)||!/^([01]\d|2[0-3]):[0-5]\d$/.test(input.time||''))throw Error('Choose a valid time.');
  if(cadence==='weekly'&&!/^[0-6]$/.test(String(input.weekday)))throw Error('Choose a weekday.');
  const [hour,minute]=input.time.split(':').map(Number);
  return {kind:'cron',expression:`${minute} ${hour} * * ${cadence==='weekly'?input.weekday:'*'}`,timezone:input.timezone};
}
