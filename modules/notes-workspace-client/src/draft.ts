// Copy on write: reads preserve rows; only modified branches are materialized.
type Node = {
    base: any;
    copy?: any;
    children: Map<PropertyKey, Node>;
    changed: Set<PropertyKey>;
    proxy: any;
};
export function draft<T extends object>(base: T) {
    const proxies = new WeakMap<object, Node>();
    const create = (value: any, parent?: Node, key?: PropertyKey): Node => {
        const node: Node = { base: value, children: new Map(), changed: new Set(), proxy: undefined };
        const mark = () => {
            if (node.copy) return;
            node.copy = Array.isArray(node.base) ? node.base.slice() : { ...node.base };
            if (parent) {
                marks.get(parent)!();
                parent.changed.add(key!);
            }
        };
        marks.set(node, mark);
        node.proxy = new Proxy(value, {
            get(_target, prop) {
                const current = node.copy ?? node.base;
                const at = Reflect.get(current, prop);
                if (!at || typeof at !== "object") return at;
                let child = node.children.get(prop);
                if (!child || child.base !== at) {
                    child = create(at, node, prop);
                    node.children.set(prop, child);
                }
                return child.proxy;
            },
            set(_target, prop, at) {
                if (Object.is((node.copy ?? node.base)[prop], at)) return true;
                mark(); node.changed.add(prop); node.children.delete(prop);
                node.copy[prop] = at;
                return true;
            },
            deleteProperty(_target, prop) {
                if (!Object.prototype.hasOwnProperty.call(node.copy ?? node.base, prop)) return true;
                mark(); node.changed.add(prop); node.children.delete(prop);
                delete node.copy[prop];
                return true;
            },
            ownKeys() { return Reflect.ownKeys(node.copy ?? node.base); },
            has(_target, prop) { return prop in (node.copy ?? node.base); },
            getOwnPropertyDescriptor(_target, prop) {
                return Object.getOwnPropertyDescriptor(node.copy ?? node.base, prop);
            },
        });
        proxies.set(node.proxy, node);
        return node;
    };
    const marks = new Map<Node, () => void>();
    const root = create(base);
    const unwrap = (value: any): any => {
        if (!value || typeof value !== "object") return value;
        const linked = proxies.get(value);
        if (linked) return finish(linked);
        let copy: any;
        for (const [key, at] of Object.entries(value)) {
            const next = unwrap(at);
            if (next !== at) {
                copy ??= Array.isArray(value) ? value.slice() : { ...value };
                copy[key] = next;
            }
        }
        return copy ?? value;
    };
    const finish = (node: Node): any => {
        if (!node.copy) return node.base;
        for (const key of node.changed) {
            if (!Object.prototype.hasOwnProperty.call(node.copy, key)) continue;
            const child = node.children.get(key);
            if (child && child.copy) node.copy[key] = finish(child);
            else {
                node.copy[key] = unwrap(node.copy[key]);
            }
        }
        return node.copy;
    };
    return {
        value: root.proxy as T,
        finish: () => finish(root) as T,
        keys(field: string): string[] {
            const child = root.children.get(field);
            const before = (base as any)[field];
            const after = (root.copy ?? base)[field];
            if (root.changed.has(field) && (!child || child.base !== before))
                return [...new Set([...Object.keys(before ?? {}), ...Object.keys(after ?? {})])];
            if (!child?.copy) return [];
            const keys = [...child.changed].filter(k => typeof k === "string") as string[];
            if (Array.isArray(before) && child.changed.has("length"))
                for (let i = child.copy.length; i < before.length; i++) keys.push(String(i));
            return [...new Set(keys)];
        },
    };
}
