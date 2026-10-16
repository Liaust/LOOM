import { Modal, ButtonComponent } from "../deps";
import type ObsidianLiveSyncPlugin from "../main";
import type { IntentClient } from "./client";
import { conflictPaths, inspectConflict, type ConflictReview } from "./conflicts";

export class NotesConflictsModal extends Modal {
    constructor(private plugin: ObsidianLiveSyncPlugin, private client: IntentClient,
        private initial?: { create?: string; path?: string }) { super(plugin.app); }
    override async onOpen() {
        this.titleEl.setText("LOOM note conflicts");
        this.contentEl.empty();
        await this.client.ready;
        const initial = this.initial; this.initial = undefined;
        if (initial?.create) { await this.recreate(initial.create); return; }
        if (initial?.path) { await this.compare(initial.path); return; }
        const recoverable = this.client.journal.state.operations.filter(op => op.state === "held" &&
            op.reason === "native_target_exists" && op.intent.operation === "create" && !op.intent.entryKind);
        for (const op of recoverable.slice(0, 20)) {
            const row = this.contentEl.createDiv();
            new ButtonComponent(row).setButtonText("Review queued creation: " + op.intent.path).onClick(() => {
                void this.recreate(op.intent.id);
            });
        }
        const paths = conflictPaths(this.client);
        if (!paths.length && !recoverable.length) this.contentEl.createEl("p", { text: "No conflicts need attention." });
        for (const path of paths) {
            const row = this.contentEl.createDiv();
            row.style.marginBottom = "8px";
            new ButtonComponent(row).setButtonText(path).onClick(() => { void this.compare(path); });
        }
    }
    private async recreate(id: string) {
        this.titleEl.setText("Review queued note creation");
        this.contentEl.empty();
        const message = this.contentEl.createEl("p", { text: "Checking the retained operation and native destination..." });
        try {
            const review = await this.client.inspectCreateRecovery(id);
            message.setText(`${review.path}: the original creation and ${review.descendants} dependent changes remain saved. ` +
                "The native destination is absent or logically deleted. Main will still refuse an occupied source path.");
            const button = new ButtonComponent(this.contentEl).setButtonText("Resume this saved creation");
            button.onClick(() => { button.setDisabled(true); void this.client.recoverCreate(review).then(() => {
                message.setText("The original creation was queued again. Its dependent changes wait for Main's acceptance; this is not yet a source receipt.");
            }).catch(error => { message.setText(error instanceof Error ? error.message : "Recovery was refused."); }); });
        } catch (error) { message.setText(error instanceof Error ? error.message : "Recovery is unavailable."); }
        new ButtonComponent(this.contentEl).setButtonText("Back").onClick(() => { void this.onOpen(); });
    }
    private async compare(path: string) {
        this.titleEl.setText(path.split("/").pop() ?? path);
        this.contentEl.empty();
        const message = this.contentEl.createEl("p", { text: "Loading comparison…" });
        let review: ConflictReview;
        try { review = await inspectConflict(this.client, path); }
        catch (e) {
            message.setText(e instanceof Error ? e.message : "Comparison is unavailable.");
            new ButtonComponent(this.contentEl).setButtonText("Refresh").onClick(() => { void this.compare(path); });
            return;
        }
        message.setText("");
        const panes = this.contentEl.createDiv();
        Object.assign(panes.style, { display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(min(100%, 240px), 1fr))", gap: "12px" });
        const pane = (label: string, text: string, editable = false) => {
            const section = panes.createDiv();
            section.createEl("h4", { text: label });
            const field = section.createEl("textarea");
            field.value = text; field.readOnly = !editable;
            field.setAttribute("aria-label", label);
            Object.assign(field.style, { width: "100%", height: "180px", resize: "vertical", boxSizing: "border-box" });
            return field;
        };
        pane("This device", review.local);
        pane("Current source", review.source);
        if (review.base !== null) pane("Original base", review.base);
        else this.contentEl.createEl("p", { text: "The original base text is no longer available on this device. Both current versions are preserved." });
        for (const leaf of review.leaves) if (leaf.text !== review.local && leaf.text !== review.source)
            pane(`Other revision ${leaf.revision.slice(0, 14)}`, leaf.text);
        const merged = pane("Merged result", review.local, true);
        const actions = this.contentEl.createDiv();
        Object.assign(actions.style, { display: "flex", flexWrap: "wrap", gap: "8px", marginTop: "12px" });
        const buttons: ButtonComponent[] = [];
        const submit = async (text: string) => {
            buttons.forEach(b => b.setDisabled(true));
            try {
                await this.client.resolve(review, text);
                message.setText("Resolution saved on this device; waiting for Main to accept it. Later edits remain preserved.");
                merged.disabled = true;
            } catch (e) {
                message.setText(e instanceof Error ? e.message : "Resolution could not be saved.");
            }
        };
        for (const [label, text] of [["Use device version", review.local], ["Use source version", review.source]])
            buttons.push(new ButtonComponent(actions).setButtonText(label).onClick(() => { void submit(text); }));
        buttons.push(new ButtonComponent(actions).setButtonText("Use merged result").onClick(() => { void submit(merged.value); }));
        new ButtonComponent(actions).setButtonText("Refresh").onClick(() => { void this.compare(path); });
        new ButtonComponent(actions).setButtonText("Later").onClick(() => this.close());
    }
}

export class NotesStatusModal extends Modal {
    private offset = 0;
    private kind: "operations" | "holds" = "operations";
    constructor(private plugin: ObsidianLiveSyncPlugin, private client: IntentClient) { super(plugin.app); }
    override async onOpen() {
        await this.client.ready;
        this.titleEl.setText("LOOM Notes status"); this.contentEl.empty();
        const view = this.client.statusDetails({ offset: this.offset, kind: this.kind });
        const s = view.summary;
        this.contentEl.createEl("p", { text: `${s.unfinished} unfinished edits: ${s.pendingPublication} to publish, ` +
            `${s.pendingAcceptance} awaiting source, ${s.blocked} blocked and ${s.waiting} waiting on dependencies.` });
        this.contentEl.createEl("p", { text: `${s.accepted} accepted; ${s.resolved} resolved. ` +
            `Unresolved safety holds: ${s.captureHolds} capture, ${s.reflectionHolds} reflection, ${s.otherHolds} other.` });
        if (s.failure || s.checkpointFailure) this.contentEl.createEl("p", { text: s.failure ?? s.checkpointFailure });
        const selector = this.contentEl.createEl("select");
        for (const [value, label] of [["operations", "Unfinished edits"], ["holds", "Unresolved safety holds"]]) {
            const option = selector.createEl("option", { text: label }); option.value = value;
        }
        selector.value = this.kind;
        selector.onchange = () => { this.kind = selector.value as typeof this.kind; this.offset = 0; void this.onOpen(); };
        this.contentEl.createEl("p", { text: `Showing ${view.groups.items.length} of ${view.groups.total}, offset ${view.groups.offset}.` });
        for (const group of view.groups.items) {
            const row = this.contentEl.createDiv();
            new ButtonComponent(row).setButtonText(`${group.path}: ${group.reason || group.kind}`)
                .onClick(() => this.item(group.id));
            if (group.operations) row.createEl("span", { text: ` (${group.operations} edits; ${group.waiting} dependent)` });
        }
        const actions = this.contentEl.createDiv();
        new ButtonComponent(actions).setButtonText("Previous").setDisabled(!this.offset)
            .onClick(() => { this.offset = Math.max(0, this.offset - 20); void this.onOpen(); });
        new ButtonComponent(actions).setButtonText("Next").setDisabled(view.groups.nextOffset === undefined)
            .onClick(() => { this.offset = view.groups.nextOffset ?? this.offset; void this.onOpen(); });
        new ButtonComponent(actions).setButtonText("Refresh").onClick(() => { this.offset = 0; void this.onOpen(); });
        new ButtonComponent(actions).setButtonText("Full diagnostics").onClick(() => {
            this.contentEl.empty(); this.contentEl.createEl("pre", { text: this.client.details() });
            new ButtonComponent(this.contentEl).setButtonText("Back").onClick(() => { void this.onOpen(); });
        });
    }
    private item(id: string, offset = 0) {
        const item = this.client.statusItem(id, { offset });
        if (!item) { this.offset = 0; void this.onOpen(); return; }
        this.contentEl.empty(); this.titleEl.setText(item.hold?.path ?? item.group.path);
        this.contentEl.createEl("p", { text: item.hold?.reason ?? item.group.reason });
        const root = item.group.operationId ? this.client.statusOperation(item.group.operationId) : undefined;
        if (root?.state === "held" && root.reason === "native_target_exists" && root.intent.operation === "create")
            new ButtonComponent(this.contentEl).setButtonText("Review saved creation").onClick(() => {
                this.close(); new NotesConflictsModal(this.plugin, this.client, { create: root.intent.id }).open();
            });
        else if (root && ["held", "conflict"].includes(root.state) && this.client.journal.state.bindings[root.intent.path]?.writable)
            new ButtonComponent(this.contentEl).setButtonText("Review text conflict").onClick(() => {
                this.close(); new NotesConflictsModal(this.plugin, this.client, { path: root.intent.path }).open();
            });
        for (const op of item.operations.items) {
            const row = this.contentEl.createDiv();
            new ButtonComponent(row).setButtonText(`${op.path}: ${op.stage} ${op.reason}`).onClick(() => {
                const full = this.client.statusOperation(op.id);
                if (full) row.createEl("pre", { text: JSON.stringify(full, null, 2) });
            });
        }
        this.contentEl.createEl("p", { text: `Showing ${item.operations.items.length} of ${item.operations.total} retained edits.` });
        if (offset) new ButtonComponent(this.contentEl).setButtonText("Previous").onClick(() => this.item(id, Math.max(0, offset - 20)));
        if (item.operations.nextOffset !== undefined) new ButtonComponent(this.contentEl).setButtonText("Next")
            .onClick(() => this.item(id, item.operations.nextOffset));
        new ButtonComponent(this.contentEl).setButtonText("Back").onClick(() => { void this.onOpen(); });
    }
}
