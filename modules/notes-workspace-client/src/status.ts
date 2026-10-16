import type { Operation, State } from "./journal";

export type StatusRuntime = { starting?: boolean; failure?: string; checkpointFailure?: string };
export type StatusSummary = Readonly<{
    phase: "starting" | "ready" | "held";
    text: string;
    operations: number;
    unfinished: number;
    pendingPublication: number;
    pendingAcceptance: number;
    blocked: number;
    blockedRoots: number;
    waiting: number;
    unknownDependencies: number;
    accepted: number;
    resolved: number;
    holds: number;
    reflectionHolds: number;
    captureHolds: number;
    otherHolds: number;
    failure?: string;
    checkpointFailure?: string;
}>;
export type StatusPageOptions = { offset?: number; limit?: number; kind?: "operations" | "holds" };
export type StatusPage<T> = { items: T[]; total: number; offset: number; omitted: number; nextOffset?: number };
export type StatusGroupKind = "blocked" | "publication" | "acceptance" | "unknown-dependency" | "hold";
export type StatusHoldKind = "reflection" | "capture" | "other";
export type StatusGroup = {
    id: string;
    kind: StatusGroupKind;
    path: string;
    reason: string;
    operationId?: string;
    operations: number;
    waiting: number;
    holdKind?: StatusHoldKind;
};
export type StatusOperation = {
    id: string;
    path: string;
    target?: string;
    operation: Operation["intent"]["operation"];
    state: Operation["state"];
    stage: "blocked" | "waiting" | "publication" | "acceptance";
    reason: string;
    predecessor?: string;
    dependencies: number;
};
export type StatusItem = {
    group: StatusGroup;
    operations: StatusPage<StatusOperation>;
    hold?: { path: string; reason: string; kind: StatusHoldKind };
    dependency?: { id: string; reason: string };
};
type Wait = { id: string; reason: string };
type Group = {
    id: string;
    kind: StatusGroupKind;
    path: string;
    reason: string;
    root?: Operation;
    members: Operation[];
    waiting: number;
    holdKind?: StatusHoldKind;
    dependency?: Wait;
};
const active = (op: Operation) => op.state !== "applied" && !op.resolvedBy;
const blocked = (op: Operation) => op.state === "held" || op.state === "conflict";
const key = (kind: string, id: string) => JSON.stringify([kind, id]);
const compact = (text: string) => text.length > 160 ? text.slice(0, 157) + "..." : text;
const captureReasons = new Set(["missing_writable_binding", "outside_collection", "rename_target_collision",
    "payload_limit", "reference_only", "directory_base_unavailable"]);
function holdKind(reason: string): StatusHoldKind {
    if (/^(event_|capture_|predecessor_capture_)/.test(reason) || captureReasons.has(reason)) return "capture";
    if (/^(reflection_|layout_|directory_reflection_)/.test(reason) ||
        ["interrupted_reflection", "missing_local_file", "binding_base_collision",
            "native_conflict_requires_resolution", "directory_client_unavailable", "path_change_preserves_local_edit"].includes(reason))
        return "reflection";
    return "other";
}
function page<T, U>(values: readonly T[], options: StatusPageOptions, render: (value: T) => U): StatusPage<U> {
    const offset = Number.isFinite(options.offset) ? Math.max(0, Math.trunc(options.offset!)) : 0;
    const limit = Number.isFinite(options.limit) ? Math.min(100, Math.max(1, Math.trunc(options.limit!))) : 20;
    const items = values.slice(offset, offset + limit).map(render);
    return { items, total: values.length, offset, omitted: values.length - items.length,
        ...(offset + items.length < values.length ? { nextOffset: offset + items.length } : {}) };
}
function renderGroup(group: Group): StatusGroup {
    return { id: group.id, kind: group.kind, path: compact(group.path), reason: compact(group.reason),
        ...(group.root ? { operationId: group.root.intent.id } : {}), operations: group.members.length,
        waiting: group.waiting, ...(group.holdKind ? { holdKind: group.holdKind } : {}) };
}

// This is a read-only projection, not a publisher eligibility or resolution rule.
// Journal updates preserve old snapshots and replace modified branches. Repeated
// UI events (including checkpoint-only changes) need not walk retained history.
export class NotesStatus {
    private indexedOperations?: State["operations"];
    private indexedHolds?: State["holds"];
    private byId = new Map<string, Operation>();
    private waits = new Map<string, Wait[]>();
    private operationGroups: Group[] = [];
    private holdGroups: Group[] = [];
    private groups: Group[] = [];
    private groupsById = new Map<string, Group>();
    private counts = { operations: 0, unfinished: 0, pendingPublication: 0, pendingAcceptance: 0,
        blocked: 0, blockedRoots: 0, waiting: 0, unknownDependencies: 0, accepted: 0, resolved: 0 };
    private holdCounts = { holds: 0, reflectionHolds: 0, captureHolds: 0, otherHolds: 0 };
    private cachedSummary?: StatusSummary;

    private index(state?: State) {
        let changed = false;
        if (this.indexedOperations !== state?.operations) {
            this.indexOperations(state?.operations ?? []);
            this.indexedOperations = state?.operations;
            changed = true;
        }
        if (this.indexedHolds !== state?.holds) {
            this.holdCounts = { holds: 0, reflectionHolds: 0, captureHolds: 0, otherHolds: 0 };
            this.holdGroups = Object.entries(state?.holds ?? {}).map(([path, reason]) => {
                const kind = holdKind(reason);
                this.holdCounts.holds++;
                if (kind === "reflection") this.holdCounts.reflectionHolds++;
                else if (kind === "capture") this.holdCounts.captureHolds++;
                else this.holdCounts.otherHolds++;
                // A missing operation, an old path or a matching accepted record
                // cannot resolve the independent hold map's safety evidence.
                return { id: key("hold", path), kind: "hold", path, reason, members: [], waiting: 0, holdKind: kind };
            });
            this.indexedHolds = state?.holds;
            changed = true;
        }
        if (changed) {
            this.groups = [...this.operationGroups.filter(g => g.kind === "blocked" || g.kind === "unknown-dependency"),
                ...this.holdGroups, ...this.operationGroups.filter(g => g.kind === "publication" || g.kind === "acceptance")];
            this.groupsById = new Map(this.groups.map(group => [group.id, group]));
            this.cachedSummary = undefined;
        }
    }
    private indexOperations(operations: Operation[]) {
        this.byId = new Map();
        this.waits = new Map();
        this.counts = { operations: operations.length, unfinished: 0, pendingPublication: 0, pendingAcceptance: 0,
            blocked: 0, blockedRoots: 0, waiting: 0, unknownDependencies: 0, accepted: 0, resolved: 0 };
        const pending: Operation[] = [];
        for (const op of operations) {
            this.byId.set(op.intent.id, op);
            if (op.resolvedBy) this.counts.resolved++;
            else if (op.state === "applied") this.counts.accepted++;
            else { pending.push(op); this.counts.unfinished++; }
        }
        const recovering = new Map<string, string | undefined>();
        const recoveryRoot = (id: string | undefined): string | undefined => {
            const chain: string[] = [], seen = new Set<string>();
            let root: string | undefined;
            while (id && !seen.has(id)) {
                if (recovering.has(id)) { root = recovering.get(id); break; }
                seen.add(id); chain.push(id);
                const ancestor = this.byId.get(id);
                if (!ancestor) break;
                if (ancestor.nativeRecovery && ancestor.state !== "applied") { root = id; break; }
                id = ancestor.intent.predecessor;
            }
            for (const child of chain) recovering.set(child, root);
            return root;
        };
        for (const op of pending) {
            const waits: Wait[] = [];
            const seen = new Set<string>();
            if (op.state === "uploaded") { this.waits.set(op.intent.id, waits); continue; }
            const predecessor = op.intent.predecessor;
            if (predecessor) {
                const parent = this.byId.get(predecessor);
                if (!parent || !parent.publication || blocked(parent)) {
                    waits.push({ id: predecessor, reason: parent ? "predecessor_publication_pending" : "missing_predecessor" });
                    seen.add(predecessor);
                }
                const root = recoveryRoot(predecessor);
                if (root && !seen.has(root)) {
                    waits.push({ id: root, reason: "recreated_root_acceptance_pending" });
                    seen.add(root);
                }
            }
            for (const id of op.intent.dependencies ?? []) {
                if (this.byId.get(id)?.state !== "applied" && !seen.has(id))
                    waits.push({ id, reason: this.byId.has(id) ? "dependency_acceptance_pending" : "missing_dependency" });
                seen.add(id);
            }
            this.waits.set(op.intent.id, waits);
        }
        const roots = new Map<string, Group>();
        const owners = new Map<string, Group>();
        const make = (id: string, kind: StatusGroupKind, path: string, reason: string, root?: Operation, dependency?: Wait) => {
            let group = roots.get(id);
            if (!group) {
                group = { id, kind, path, reason, root, dependency, members: [], waiting: 0 };
                roots.set(id, group);
            }
            return group;
        };
        // Iterative path compression keeps long retained edit chains stack-safe.
        const owner = (first: Operation): Group => {
            const chain: string[] = [];
            const seen = new Map<string, number>();
            let op = first;
            let group: Group;
            for (;;) {
                const id = op.intent.id;
                const known = owners.get(id);
                if (known) { group = known; break; }
                const cycleAt = seen.get(id);
                if (cycleAt !== undefined) {
                    const cycle = chain.slice(cycleAt).sort()[0];
                    group = make(key("cycle", cycle), "unknown-dependency", this.byId.get(cycle)!.intent.path,
                        "dependency_cycle", undefined, { id: cycle, reason: "dependency_cycle" });
                    break;
                }
                seen.set(id, chain.length);
                chain.push(id);
                if (blocked(op)) {
                    group = make(key("operation", id), "blocked", op.intent.path, op.reason ?? op.state, op);
                    break;
                }
                const waits = this.waits.get(id) ?? [];
                // Prefer a directly blocked dependency; otherwise use the first
                // unmet edge. Each operation belongs to exactly one primary group.
                const wait = waits.find(wait => { const parent = this.byId.get(wait.id); return parent && active(parent) && blocked(parent); }) ?? waits[0];
                if (!wait) {
                    group = make(key("operation", id), op.state === "uploaded" ? "acceptance" : "publication",
                        op.intent.path, op.reason ?? (op.state === "uploaded" ? "source_acceptance_pending" : "publication_pending"), op);
                    break;
                }
                const parent = this.byId.get(wait.id);
                if (!parent || !active(parent)) {
                    group = make(key("dependency", wait.id), "unknown-dependency", op.intent.path,
                        parent ? "dependency_receipt_unavailable" : wait.reason, undefined, wait);
                    break;
                }
                op = parent;
            }
            for (const id of chain) owners.set(id, group);
            return group;
        };
        for (const op of pending) {
            const group = owner(op);
            group.members.push(op);
            if (blocked(op)) this.counts.blocked++;
            else if (this.waits.get(op.intent.id)!.length) {
                this.counts.waiting++;
                group.waiting++;
                if (group.kind === "unknown-dependency" || this.waits.get(op.intent.id)!.some(wait => {
                    const parent = this.byId.get(wait.id);
                    return !parent || !active(parent);
                })) this.counts.unknownDependencies++;
            } else if (op.state === "uploaded") this.counts.pendingAcceptance++;
            else this.counts.pendingPublication++;
        }
        const rank: Record<StatusGroupKind, number> = { blocked: 0, "unknown-dependency": 1, hold: 2, publication: 3, acceptance: 4 };
        this.operationGroups = [...roots.values()].sort((a, b) => rank[a.kind] - rank[b.kind]);
        this.counts.blockedRoots = this.operationGroups.filter(group => group.kind === "blocked").length;
    }
    summary(state?: State, runtime: StatusRuntime = {}): StatusSummary {
        this.index(state);
        const phase = runtime.failure ? "held" : runtime.starting || !state ? "starting" : "ready";
        const previous = this.cachedSummary;
        if (previous && previous.phase === phase && previous.failure === runtime.failure &&
            previous.checkpointFailure === runtime.checkpointFailure) return previous;
        const c = this.counts, h = this.holdCounts;
        const text = phase === "starting" ? "LOOM starting" : phase === "held" ? "LOOM held: " + compact(runtime.failure!) :
            `LOOM: ${c.pendingPublication} to publish; ${c.pendingAcceptance} awaiting source; ${c.blocked} blocked; ${c.waiting} waiting; ${h.holds} holds`;
        this.cachedSummary = Object.freeze({ phase, text: text + (runtime.checkpointFailure ? "; recovery export failed" : ""),
            ...c, ...h, ...(runtime.failure ? { failure: runtime.failure } : {}),
            ...(runtime.checkpointFailure ? { checkpointFailure: runtime.checkpointFailure } : {}) });
        return this.cachedSummary;
    }
    overview(state?: State, options: StatusPageOptions = {}, runtime: StatusRuntime = {}) {
        const summary = this.summary(state, runtime);
        // Safety holds precede ordinary pending work, even in a large backlog.
        const selected = options.kind === "operations" ? this.groups.filter(group => group.kind !== "hold") :
            options.kind === "holds" ? this.holdGroups : this.groups;
        const groups = page(selected, options, renderGroup);
        return { summary, groups };
    }
    item(state: State | undefined, id: string, options: StatusPageOptions = {}): StatusItem | undefined {
        this.index(state);
        const group = this.groupsById.get(id);
        if (!group) return;
        return { group: renderGroup(group), operations: page(group.members, options, (op): StatusOperation => {
            const stage = blocked(op) ? "blocked" : this.waits.get(op.intent.id)?.length ? "waiting" :
                op.state === "uploaded" ? "acceptance" : "publication";
            return { id: op.intent.id, path: compact(op.intent.path), ...(op.intent.target ? { target: compact(op.intent.target) } : {}),
                operation: op.intent.operation, state: op.state, stage, reason: compact(op.reason ?? this.waits.get(op.intent.id)?.[0]?.reason ?? ""),
                predecessor: op.intent.predecessor, dependencies: op.intent.dependencies?.length ?? 0 };
        }), ...(group.holdKind ? { hold: { path: group.path, reason: group.reason, kind: group.holdKind } } : {}),
        ...(group.dependency ? { dependency: { ...group.dependency } } : {}) };
    }
    // Explicit full-item access only; overview and group pages never read payloads.
    operation(state: State | undefined, id: string): Readonly<Operation> | undefined {
        this.index(state);
        return this.byId.get(id);
    }
}
