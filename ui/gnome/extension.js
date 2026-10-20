// Agent Mission Control for GNOME Shell (45+): a panel indicator counting the
// coding-agent workflows that need you, and a menu to answer them. A thin
// client of the agent-mission-control daemon (see client.js); all logic is in the
// daemon and model.js. Agent text is untrusted: it only ever goes into plain
// St.Labels (never markup), cleaned of control characters.
import Clutter from 'gi://Clutter';
import GLib from 'gi://GLib';
import GObject from 'gi://GObject';
import Gio from 'gi://Gio';
import Pango from 'gi://Pango';
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
        super._init(0.0, 'Agent Mission Control');
        this._client = new Client();
        this._cancellable = new Gio.Cancellable();
        this._workflows = [];
        this._reachable = false;
        this._loaded = false;
        this._signatures = null;
        this._inFlight = false;
        this._dotTimer = 0;
        this._runningDots = [];
        this._dotFrame = 0;
        this._dotFrames = ['●··', '·●·', '··●', '·●·'];

        const box = new St.BoxLayout({style_class: 'panel-status-menu-box'});
        this._icon = new St.Icon({icon_name: 'utilities-terminal-symbolic', style_class: 'system-status-icon'});
        this._count = new St.Label({text: '', y_align: Clutter.ActorAlign.CENTER, style_class: 'agent-mission-control-count'});
        box.add_child(this._icon);
        box.add_child(this._count);
        this.add_child(box);

        this.menu.connect('open-state-changed', (_menu, open) => {
            if (open) {
                this._rebuild();
                this._refresh();
            } else
                this._stopCardDots();
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
            this._error = r.status === 0 ? 'agent-mission-control is not running'
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
                logError(e, 'agent-mission-control');
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
                actor.add_style_class_name('agent-mission-control-attention');
            else
                actor.remove_style_class_name('agent-mission-control-attention');
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
        this._stopCardDots();
        this.menu.removeAll();
        this.menu.box.add_style_class_name('agent-mission-control-menu');

        const header = new PopupMenu.PopupBaseMenuItem({
            reactive: false,
            can_focus: false,
            style_class: 'agent-mission-control-header',
        });
        const heading = new St.BoxLayout({vertical: true, x_expand: true});
        heading.add_child(new St.Label({
            text: 'Agent Mission Control',
            style_class: 'agent-mission-control-header-title',
        }));
        heading.add_child(new St.Label({
            text: !this._loaded ? 'connecting…'
                : this._reachable ? model.summary(this._workflows) : this._error,
            style_class: `agent-mission-control-header-summary${this._reachable ? '' : ' agent-mission-control-dim'}`,
        }));
        header.add_child(heading);
        this.menu.addMenuItem(header);

        if (this._loaded && !this._reachable) {
            this._infoItem(this.menu, `Start the daemon: amc install (or systemctl --user start agent-mission-control)\nLooking for it at http://127.0.0.1:4200`);
            return;
        }
        if (this._reachable && !this._workflows.length) {
            this._infoItem(this.menu, 'No workflows yet: start an opencode or claude session.');
            return;
        }
        for (const w of this._workflows) {
            try {
                this.menu.addMenuItem(this._workflowItem(w));
            } catch (e) {
                logError(e, `agent-mission-control: couldn't render workflow ${model.clean(w.title || w.source_id, 80)}`);
                this._infoItem(this.menu, `Couldn't display ${model.clean(w.title || w.source_id, 80)}. See the shell log for details.`);
            }
        }
    }

    _infoItem(menu, text) {
        const item = new PopupMenu.PopupMenuItem('', {reactive: false});
        item.label.text = model.clean(text, 400);
        item.label.clutter_text.line_wrap = true;
        menu.addMenuItem(item);
        return item;
    }

    _workflowItem(w) {
        const card = new PopupMenu.PopupBaseMenuItem({
            reactive: false,
            can_focus: false,
            style_class: 'agent-mission-control-card',
        });
        const content = new St.BoxLayout({
            vertical: true,
            x_expand: true,
            style_class: 'agent-mission-control-card-content',
        });
        card.add_child(content);

        const heading = new St.BoxLayout({
            x_expand: true,
            style_class: 'agent-mission-control-card-heading',
        });
        const statusClass = `agent-mission-control-status agent-mission-control-status-${this._statusClass(w.status)}`;
        if (w.status === 'running') {
            const dots = new St.Label({
                text: this._dotFrames[0],
                y_align: Clutter.ActorAlign.CENTER,
                style_class: `${statusClass} agent-mission-control-running-dots`,
            });
            heading.add_child(dots);
            if (this.menu.isOpen && !w.stale)
                this._animateCardDots(dots);
        } else {
            const statusIcon = new St.Icon({
                icon_name: {
                    needs_input: 'dialog-question-symbolic',
                    stuck: 'media-playback-pause-symbolic',
                    failed: 'dialog-error-symbolic',
                    done: 'object-select-symbolic',
                }[w.status] || 'dialog-information-symbolic',
                style_class: statusClass,
            });
            heading.add_child(statusIcon);
        }

        const title = new St.Label({
            text: model.clean(w.title || w.source_id, 80),
            x_expand: true,
            y_align: Clutter.ActorAlign.CENTER,
            style_class: 'agent-mission-control-card-title',
        });
        heading.add_child(title);

        const status = new St.Label({
            text: `${model.statusLabel(w)} · ${model.age(w.updated_at)}`,
            y_align: Clutter.ActorAlign.CENTER,
            style_class: `agent-mission-control-card-status agent-mission-control-status-${this._statusClass(w.status)}`,
        });
        heading.add_child(status);

        if (w.pids && w.pids.length && !w.stale) {
            const focus = new St.Button({
                can_focus: true,
                style_class: 'agent-mission-control-terminal',
                child: new St.Icon({icon_name: 'utilities-terminal-symbolic'}),
            });
            focus.connect('clicked', () => {
                if (this._focus(w.pids))
                    this.menu.close();
            });
            heading.add_child(focus);
        }
        content.add_child(heading);

        const location = model.tilde(w.location, HOME);
        if (location) {
            const label = new St.Label({
                text: model.clean(location, 240),
                x_expand: true,
                style_class: 'agent-mission-control-card-location',
            });
            label.clutter_text.ellipsize = Pango.EllipsizeMode.MIDDLE;
            content.add_child(label);
        }

        if (model.needsYou(w))
            card.add_style_class_name('agent-mission-control-card-attention');
        if (w.stale)
            card.opacity = 150;

        const {ask, question, several} = model.firstQuestion(w);
        const canInput = model.canInput(w);
        if (question) {
            this._cardLabel(content, (question.header ? `${question.header}: ` : '') + question.question, 'agent-mission-control-card-question');
            const options = question.options || [];
            if (canInput && options.length)
                this._choiceButtons(content, w, ask.id, options, question.multiple, ask.kind === 'permission');
            else if (!canInput && options.length)
                this._cardLabel(content, `Options: ${options.map(opt => model.clean(opt, 80)).join(' · ')}`, 'agent-mission-control-card-secondary');
        } else if (w.stale) {
            this._cardLabel(content, w.stale_reason || 'source unreachable', 'agent-mission-control-card-message');
        } else if (several) {
            const questions = (ask.questions || []).map(q => `• ${model.clean(q.question, 300)}`).join('\n');
            this._cardLabel(content, `${questions}\nAnswer in its terminal${w.pids && w.pids.length ? ' (terminal button jumps there)' : ''}.`, 'agent-mission-control-card-secondary');
        } else if (w.message) {
            this._cardLabel(content, w.message, 'agent-mission-control-card-message');
        }

        if (canInput && question && question.free_text)
            content.add_child(this._entry('or type an answer, Enter to send', text => this._answer(w, ask.id, text)));
        else if (model.canFollowUp(w)) {
            const hint = w.status === 'done' ? 'follow up…'
                : w.status === 'needs_input' ? 'reply…' : 'tell it how to continue…';
            content.add_child(this._entry(hint, text => this._send(w, text)));
        }

        if (!question && ask && !several && !w.stale) {
            const questions = (ask.questions || []).map(q => `• ${model.clean(q.question, 300)}`).join('\n');
            this._cardLabel(content, `${questions}\nAnswer in its terminal${w.pids && w.pids.length ? ' (terminal button jumps there)' : ''}.`, 'agent-mission-control-card-secondary');
        } else if ((!canInput || several) && !w.stale && (ask || w.status === 'needs_input') && !several) {
            this._cardLabel(content, `Answer in its terminal${w.pids && w.pids.length ? ' (terminal button jumps there)' : ''}.`, 'agent-mission-control-card-secondary');
        }

        return card;
    }

    _animateCardDots(dots) {
        this._runningDots.push(dots);
        if (this._dotTimer)
            return;

        this._dotTimer = GLib.timeout_add(GLib.PRIORITY_DEFAULT, 180, () => {
            if (!this.menu.isOpen || !this._runningDots.length) {
                this._dotTimer = 0;
                for (const label of this._runningDots)
                    label.text = this._dotFrames[0];
                this._runningDots = [];
                this._dotFrame = 0;
                return GLib.SOURCE_REMOVE;
            }
            this._dotFrame = (this._dotFrame + 1) % this._dotFrames.length;
            for (const label of this._runningDots)
                label.text = this._dotFrames[this._dotFrame];
            return GLib.SOURCE_CONTINUE;
        });
    }

    _stopCardDots() {
        if (this._dotTimer) {
            GLib.Source.remove(this._dotTimer);
            this._dotTimer = 0;
        }
        for (const label of this._runningDots)
            label.text = this._dotFrames[0];
        this._runningDots = [];
        this._dotFrame = 0;
    }

    _statusClass(status) {
        return String(status || 'unknown').replaceAll('_', '-');
    }

    _cardLabel(parent, text, styleClass) {
        const label = new St.Label({
            text: model.clean(text, 1000),
            x_expand: true,
            style_class: styleClass,
        });
        label.clutter_text.line_wrap = true;
        parent.add_child(label);
        return label;
    }

    _button(label, styleClass, onClick) {
        const button = new St.Button({
            can_focus: true,
            style_class: styleClass,
            child: new St.Label({text: label}),
        });
        button.connect('clicked', onClick);
        return button;
    }

    _choiceButtons(parent, w, askId, options, multiple, permission) {
        const row = new St.BoxLayout({style_class: 'agent-mission-control-choices'});
        parent.add_child(row);
        const cleanOptions = options.map(opt => model.clean(opt, 80));
        if (!multiple) {
            for (let i = 0; i < options.length; i++) {
                const opt = options[i];
                const button = this._button(cleanOptions[i],
                    `agent-mission-control-choice${permission && opt === 'reject' ? ' agent-mission-control-choice-reject' : ''}`,
                    () => {
                        this._answer(w, askId, opt);
                        this.menu.close();
                    });
                row.add_child(button);
            }
            return;
        }

        let picked = [];
        const send = this._button('Send selected', 'agent-mission-control-choice agent-mission-control-send', () => {
            this._answer(w, askId, picked);
            this.menu.close();
        });
        send.reactive = false;
        send.can_focus = false;
        send.opacity = 128;
        for (let i = 0; i < options.length; i++) {
            const opt = options[i];
            const label = new St.Label({text: cleanOptions[i]});
            const button = new St.Button({
                can_focus: true,
                style_class: `agent-mission-control-choice${permission && opt === 'reject' ? ' agent-mission-control-choice-reject' : ''}`,
                child: label,
            });
            button.connect('clicked', () => {
                picked = model.togglePick(picked, opt, options);
                const selected = picked.includes(opt);
                label.text = `${selected ? '✓ ' : ''}${cleanOptions[i]}`;
                if (selected)
                    button.add_style_class_name('agent-mission-control-choice-selected');
                else
                    button.remove_style_class_name('agent-mission-control-choice-selected');
                const canSend = picked.length > 0;
                send.reactive = canSend;
                send.can_focus = canSend;
                send.opacity = canSend ? 255 : 128;
            });
            row.add_child(button);
        }
        row.add_child(send);
    }

    _entry(hint, onSubmit) {
        const entry = new St.Entry({
            hint_text: hint,
            can_focus: true,
            x_expand: true,
            style_class: 'agent-mission-control-entry',
        });
        entry.clutter_text.connect('activate', () => {
            const text = entry.get_text().trim();
            if (text) {
                entry.set_text('');
                onSubmit(text);
                this.menu.close();
            }
        });
        return entry;
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
            Main.notify('Agent Mission Control', model.clean(why));
        } else {
            log(`agent-mission-control: ${okText}`);
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
                return true;
            }
        }
        Main.notify('Agent Mission Control', 'No window found for that agent.');
        return false;
    }

    destroy() {
        if (this._timer) {
            GLib.Source.remove(this._timer);
            this._timer = 0;
        }
        this._stopCardDots();
        this._cancellable.cancel();
        this._client.destroy();
        super.destroy();
    }
});

export default class AgentMissionControlExtension extends Extension {
    enable() {
        this._indicator = new Indicator();
        Main.panel.addToStatusArea(this.uuid, this._indicator);
    }

    disable() {
        this._indicator?.destroy();
        this._indicator = null;
    }
}
