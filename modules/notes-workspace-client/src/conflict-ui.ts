import { Modal, ButtonComponent } from "../deps";
import type ObsidianLiveSyncPlugin from "../main";
import type { IntentClient } from "./client";
import { conflictPaths, inspectConflict, type ConflictReview } from "./conflicts";

export class NotesConflictsModal extends Modal {
    constructor(private plugin: ObsidianLiveSyncPlugin, private client: IntentClient) { super(plugin.app); }
    override async onOpen() {
        this.titleEl.setText("LOOM note conflicts");
        this.contentEl.empty();
        await this.client.ready;
        const paths = conflictPaths(this.client);
        if (!paths.length) this.contentEl.createEl("p", { text: "No conflicts need attention." });
        for (const path of paths) {
            const row = this.contentEl.createDiv();
            row.style.marginBottom = "8px";
            new ButtonComponent(row).setButtonText(path).onClick(() => { void this.compare(path); });
        }
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
