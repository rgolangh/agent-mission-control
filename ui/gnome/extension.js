// Mission control for GNOME Shell (45+): a panel indicator counting the
// coding-agent workflows that need you, and a menu to answer them. A thin
// client of the mission-control daemon (see client.js); all logic is in the
// daemon and model.js. Agent text is untrusted: it only ever goes into plain
// St.Labels (never markup), cleaned of control characters.
import Clutter from 'gi://Clutter';
import GLib from 'gi://GLib';
import GObject from 'gi://GObject';
import Gio from 'gi://Gio';
import St from 'gi://St';

import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';
import * as Main from 'resource:///org/gnome/shell/ui/main.js';
import * as PanelMenu from 'resource:///org/gnome/shell/ui/panelMenu.js';
import * as PopupMenu from 'resource:///org/gnome/shell/ui/popupMenu.js';

import {Client} from './client.js';
import * as model from './model.js';

const POLL_SECONDS = 2;
const HOME = GLib.get_home_dir();

const Indicator = GObject.registerClass(
class Indicator extends PanelMenu.Button {
    _init() {
        super._init(0.0, 'Mission control');
        this._client = new Client();
        this._cancellable = new Gio.Cancellable();
        this._workflows = [];
        this._reachable = false;
        this._loaded = false;
        this._signatures = null;
        this._inFlight = false;

        const box = new St.BoxLayout({style_class: 'panel-status-menu-box'});
        this._icon = new St.Icon({icon_name: 'utilities-terminal-symbolic', style_class: 'system-status-icon'});
        this._count = new St.Label({text: '', y_align: Clutter.ActorAlign.CENTER, style_class: 'mission-control-count'});
        box.add_child(this._icon);
        box.add_child(this._count);
        this.add_child(box);

        this.menu.connect('open-state-changed', (_menu, open) => {
            if (open) {
                this._rebuild();
                this._refresh();
            }
        });

        this._rebuild();
        this._refresh();
        this._timer = GLib.timeout_add_seconds(GLib.PRIORITY_DEFAULT, POLL_SECONDS, () => {
            this._refresh();
            return GLib.SOURCE_CONTINUE;
        });
    }

    async _refresh() {
        if (this._inFlight)
            return;
        this._inFlight = true;
        try {
            const r = await this._client.workflows(this._cancellable);
            this._loaded = true;
            this._reachable = r.status === 200;
            this._error = r.status === 0 ? 'mission-control is not running'
                : r.status === 401 ? "not authorized: can't read the daemon's token file"
                    : r.status !== 200 ? `daemon returned ${r.status}` : '';
            if (this._reachable) {
                this._workflows = model.sortByUrgency(r.workflows);
                const {next, fresh} = model.newlyNeedingYou(this._workflows, this._signatures);
                this._signatures = next;
                fresh.forEach(w => this._notify(w));
            }
            this._updatePanel();
            // Don't rebuild under the pointer while the menu is open: an
            // entry being typed into would vanish. It is rebuilt on open.
            if (!this.menu.isOpen)
                this._rebuild();
        } catch (e) {
            if (!e.matches?.(Gio.IOErrorEnum, Gio.IOErrorEnum.CANCELLED))
                logError(e, 'mission-control');
        } finally {
            this._inFlight = false;
        }
    }

    _updatePanel() {
        const c = model.counts(this._workflows);
        const n = c.attention || c.running;
        this._count.text = this._reachable && n ? String(n) : '';
        const attention = this._reachable && c.attention > 0;
        for (const actor of [this._icon, this._count]) {
            if (attention)
                actor.add_style_class_name('mission-control-attention');
            else
                actor.remove_style_class_name('mission-control-attention');
        }
        this._icon.opacity = this._reachable ? 255 : 120;
    }

    _notify(w) {
        const {question} = model.firstQuestion(w);
        const heading = {needs_input: 'Agent needs you', stuck: 'Agent is stuck', failed: 'Agent failed'}[w.status];
        const title = model.clean(w.title || w.source_id, 80);
        const body = model.clean(question ? question.question : w.message);
        Main.notify(`${heading}: ${title}`, body);
    }

    _rebuild() {
        this.menu.removeAll();

        const header = new PopupMenu.PopupMenuItem('', {reactive: false});
        header.label.text = !this._loaded ? 'connecting…'
            : this._reachable ? model.summary(this._workflows) : this._error;
        this.menu.addMenuItem(header);
        this.menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());

        if (this._loaded && !this._reachable) {
            this._infoItem(this.menu, 'Start it with: mission-control install (or systemctl --user start mission-control)');
            return;
        }
        if (this._reachable && !this._workflows.length) {
            this._infoItem(this.menu, 'No workflows yet: start an opencode or claude session.');
            return;
        }
        for (const w of this._workflows)
            this.menu.addMenuItem(this._workflowItem(w));
    }

    _infoItem(menu, text) {
        const item = new PopupMenu.PopupMenuItem('', {reactive: false});
        item.label.text = model.clean(text, 400);
        item.label.clutter_text.line_wrap = true;
        menu.addMenuItem(item);
        return item;
    }

    _workflowItem(w) {
        const sub = new PopupMenu.PopupSubMenuMenuItem('', true);
        sub.label.text = `${model.clean(w.title || w.source_id, 60)}`;
        sub.icon.icon_name = {
            needs_input: 'dialog-question-symbolic',
            stuck: 'media-playback-pause-symbolic',
            failed: 'dialog-error-symbolic',
            running: 'content-loading-symbolic',
            done: 'object-select-symbolic',
        }[w.status] || 'dialog-information-symbolic';
        if (model.needsYou(w))
            sub.add_style_class_name('mission-control-attention');
        if (w.stale)
            sub.opacity = 150;

        const where = [model.tilde(w.location, HOME), `${model.statusLabel(w)} · ${model.age(w.updated_at)}`].filter(Boolean);
        this._infoItem(sub.menu, where.join('   '));

        const {ask, question, several} = model.firstQuestion(w);
        const canInput = model.canInput(w);
        if (question) {
            this._infoItem(sub.menu, (question.header ? `${question.header}: ` : '') + question.question);
            if (canInput && question.multiple)
                this._multiSelect(sub.menu, w, ask.id, question.options || []);
            else if (canInput) {
                for (const opt of question.options || []) {
                    const item = new PopupMenu.PopupMenuItem('');
                    item.label.text = `→ ${model.clean(opt, 80)}`;
                    item.connect('activate', () => this._answer(w, ask.id, opt));
                    sub.menu.addMenuItem(item);
                }
            }
        } else if (several) {
            // Neither the menu nor the CLI can answer a multi-question ask.
            this._infoItem(sub.menu, ask.questions.map(q => `• ${q.question}`).join('\n'));
        } else if (w.stale) {
            this._infoItem(sub.menu, w.stale_reason || 'source unreachable');
        } else if (w.message) {
            this._infoItem(sub.menu, w.message);
        }

        if (canInput && question && question.free_text)
            sub.menu.addMenuItem(this._entryItem('type an answer, Enter', text => this._answer(w, ask.id, text)));
        else if (model.canFollowUp(w))
            sub.menu.addMenuItem(this._entryItem('follow up / tell it how to continue, Enter', text => this._send(w, text)));

        if ((!canInput || several) && !w.stale && (ask || w.status === 'needs_input'))
            this._infoItem(sub.menu, 'Answer in its terminal.');

        if (w.pids && w.pids.length && !w.stale) {
            const focus = new PopupMenu.PopupMenuItem('Focus terminal');
            focus.connect('activate', () => this._focus(w.pids));
            sub.menu.addMenuItem(focus);
        }
        return sub;
    }

    // A question that allows several options: each option toggles a check
    // mark (without closing the menu) and "Send selected" answers with all
    // the picks at once.
    _multiSelect(menu, w, askId, options) {
        let picked = [];
        const send = new PopupMenu.PopupMenuItem('Send selected');
        send.setSensitive(false);
        send.connect('activate', () => this._answer(w, askId, picked));

        for (const opt of options) {
            const item = new PopupMenu.PopupMenuItem('');
            item.label.text = model.clean(opt, 80);
            item.setOrnament(PopupMenu.Ornament.NONE);
            // Shadow activate on this instance: the default emits 'activate',
            // which closes the whole menu after one pick.
            item.activate = () => {
                picked = model.togglePick(picked, opt, options);
                item.setOrnament(picked.includes(opt) ? PopupMenu.Ornament.CHECK : PopupMenu.Ornament.NONE);
                send.setSensitive(picked.length > 0);
            };
            menu.addMenuItem(item);
        }
        menu.addMenuItem(send);
    }

    _entryItem(hint, onSubmit) {
        const item = new PopupMenu.PopupBaseMenuItem({reactive: false, can_focus: false});
        const entry = new St.Entry({hint_text: hint, can_focus: true, x_expand: true, style_class: 'mission-control-entry'});
        entry.clutter_text.connect('activate', () => {
            const text = entry.get_text().trim();
            if (text) {
                entry.set_text('');
                onSubmit(text);
                this.menu.close();
            }
        });
        item.add_child(entry);
        return item;
    }

    async _answer(w, askId, choice) {
        const r = await this._client.answer(w.source_id, askId, choice, this._cancellable);
        this._report(r, `answered: ${[].concat(choice).join(', ')}`);
    }

    async _send(w, text) {
        const r = await this._client.send(w.source_id, text, this._cancellable);
        this._report(r, 'sent');
    }

    _report(r, okText) {
        if (r.status !== 200) {
            const why = r.status === 0 ? 'daemon unreachable' : (r.data && r.data.error) || `error ${r.status}`;
            Main.notify('Mission control', model.clean(why));
        } else {
            log(`mission-control: ${okText}`);
        }
        this._refresh();
    }

    // Each workflow carries `pids`: the agent process and its ancestors
    // (agent → shell → terminal). The nearest one that owns a window is the
    // agent's terminal.
    _focus(pids) {
        const windows = global.get_window_actors().map(a => a.meta_window);
        for (const pid of pids) {
            const win = windows.find(mw => mw.get_pid() === pid);
            if (win) {
                Main.activateWindow(win);
                return;
            }
        }
        Main.notify('Mission control', 'No window found for that agent.');
    }

    destroy() {
        if (this._timer) {
            GLib.Source.remove(this._timer);
            this._timer = 0;
        }
        this._cancellable.cancel();
        this._client.destroy();
        super.destroy();
    }
});

export default class MissionControlExtension extends Extension {
    enable() {
        this._indicator = new Indicator();
        Main.panel.addToStatusArea(this.uuid, this._indicator);
    }

    disable() {
        this._indicator?.destroy();
        this._indicator = null;
    }
}
