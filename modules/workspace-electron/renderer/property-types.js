export const propertyTypes = [
  { value: 'text', label: 'Text', icon: 'align-left' },
  { value: 'list', label: 'List', icon: 'list' },
  { value: 'number', label: 'Number', icon: 'hash' },
  { value: 'boolean', label: 'Checkbox', icon: 'check-square' },
];

export function convertProperty(value, type) {
  if (type === 'list') return Array.isArray(value) ? value : value === null || value === '' ? [] : [String(value)];
  const scalar = Array.isArray(value) ? value.length <= 1 ? value[0] ?? '' : undefined : value;
  if (scalar === undefined) throw Error('A multi-value list cannot be converted without losing its structure. Edit it in source mode.');
  if (type === 'text') return scalar === null ? '' : String(scalar);
  if (type === 'number') {
    if (typeof scalar === 'number' && Number.isFinite(scalar)) return scalar;
    if (typeof scalar === 'string' && /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?$/i.test(scalar.trim()) && Number.isFinite(Number(scalar))) {
      const number=Number(scalar);if(!Number.isInteger(number)||Number.isSafeInteger(number))return number;
    }
    throw Error('Enter a finite number before choosing Number. The current value was preserved.');
  }
  if (type === 'boolean') {
    if (typeof scalar === 'boolean') return scalar;
    if (scalar === 'true' || scalar === 'false') return scalar === 'true';
    throw Error('Only true or false can become a checkbox. The current value was preserved.');
  }
  throw Error('Unknown property type.');
}
