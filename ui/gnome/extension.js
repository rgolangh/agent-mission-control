// Agent Mission Control for GNOME Shell (45+): a panel indicator counting the
// coding-agent workflows that need you, and a menu to answer them. A thin
// client of the agent-mission-control daemon (see client.js); all logic is in the
// daemon and model.js. Agent text is untrusted: it only ever goes into plain
// St.Labels (never markup), cleaned of control characters.
import Clutter from 'gi://Clutter';
import GLib from 'gi://GLib';
import GObject from 'gi://GObject';
import Gio from 'gi://Gio';
import Meta from 'gi://Meta';
import Pango from 'gi://Pango';
import Shell from 'gi://Shell';
import St from 'gi://St';

import * as Config from 'resource:///org/gnome/shell/misc/config.js';
import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';
import * as Main from 'resource:///org/gnome/shell/ui/main.js';
import * as PanelMenu from 'resource:///org/gnome/shell/ui/panelMenu.js';
import * as PopupMenu from 'resource:///org/gnome/shell/ui/popupMenu.js';

import {Client} from './client.js';
import * as model from './model.js';

const POLL_SECONDS = 2;
const HOME = GLib.get_home_dir();

// St.BoxLayout gained `orientation` in GNOME 48 and removed `vertical` in
// 51; metadata.json claims 45–51, so build the property each era accepts.
const VERTICAL_LAYOUT = parseInt(Config.PACKAGE_VERSION) >= 48
    ? {orientation: Clutter.Orientation.VERTICAL}
    : {vertical: true};

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
        this._cacheNotices = {};
        this._inFlight = false;
        this._view = model.VIEW_ALL;
        this._cards = new Map();
        this._cardOrder = [];
        this._cardState = new Map();
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

        this.menu.actor.connect('captured-event', (_actor, event) => this._handleMenuShortcut(event));
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
            this._down = model.daemonState({
                status: r.status,
                token: r.token,
                ...(this._reachable ? {} : this._client.probe()),
            });
            if (this._reachable) {
                this._workflows = model.sortByUrgency(r.workflows);
                const {next, fresh} = model.newlyNeedingYou(this._workflows, this._signatures);
                this._signatures = next;
                fresh.forEach(w => this._notify(w));
                for (const w of this._workflows) {
                    const u = w.token_usage;
                    if (!w.stale && u?.cached_input > 0 && u.warning_after_seconds > 0 &&
                        Date.now() >= Date.parse(u.observed_at) + u.warning_after_seconds * 1000 &&
                        this._cacheNotices[w.id] !== u.observed_at) {
                        this._cacheNotices[w.id] = u.observed_at;
                        const overdue = u.assumed_lifetime_seconds > 0 && Date.now() >= Date.parse(u.observed_at) + u.assumed_lifetime_seconds * 1000;
                        const message = overdue ? 'Cache may already be cold.' : 'Cache may expire soon; send a follow-up if needed.';
                        Main.notify('Agent cache estimate', `${model.clean(w.title || w.source_id)}: ${message} This is an estimate; actual expiry is unknown.`);
                    }
                }
            }
            this._updatePanel();
            // While the menu is open, update it in place: a full rebuild would
            // vanish an entry being typed into. Typed text, cursors and picked
            // options survive even a re-render (see _cardState).
            if (this.menu.isOpen)
                this._syncMenu();
            else
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
        // Down: a cross in the count's place, so it doesn't read as "idle".
        this._count.text = this._reachable ? (n ? String(n) : '') : this._loaded ? '✕' : '';
        const attention = this._reachable && c.attention > 0;
        for (const actor of [this._icon, this._count]) {
            if (attention)
                actor.add_style_class_name('agent-mission-control-attention');
            else
                actor.remove_style_class_name('agent-mission-control-attention');
        }
        this._icon.opacity = this._reachable ? 255 : 120;
        if (this._loaded && !this._reachable)
            this._count.add_style_class_name('agent-mission-control-dim');
        else
            this._count.remove_style_class_name('agent-mission-control-dim');
    }

    _notify(w) {
        const {question} = model.firstQuestion(w);
        const heading = {needs_input: 'Agent needs you', stuck: 'Agent is stuck', failed: 'Agent failed'}[w.status];
        const title = model.clean(w.title || w.source_id, 80);
        const body = model.clean(question ? question.question : w.message);
        Main.notify(`${heading}: ${title}`, body);
    }

    // Ctrl+1-4 switches the view, Ctrl+R refreshes; both are swallowed while
    // a reply field has focus, so they can be typed into.
    _handleMenuShortcut(event) {
        if (!this.menu.isOpen || event.type() !== Clutter.EventType.KEY_PRESS || this._menuTextEntryFocused())
            return Clutter.EVENT_PROPAGATE;

        let modifiers = event.get_state();
        modifiers &= ~Clutter.ModifierType.LOCK_MASK;
        modifiers &= ~Clutter.ModifierType.MOD2_MASK;
        modifiers &= Clutter.ModifierType.MODIFIER_MASK;
        if (modifiers !== Clutter.ModifierType.CONTROL_MASK)
            return Clutter.EVENT_PROPAGATE;

        const view = {
            [Clutter.KEY_1]: model.VIEW_ALL,
            [Clutter.KEY_2]: model.VIEW_NEEDS_YOU,
            [Clutter.KEY_3]: model.VIEW_WAITING,
            [Clutter.KEY_4]: model.VIEW_RUNNING,
        }[event.get_key_symbol()];
        if (view) {
            this._setView(view, true);
            return Clutter.EVENT_STOP;
        }

        if (event.get_key_symbol() === Clutter.KEY_r) {
            this._viewButtons.get(this._view)?.button.grab_key_focus();
            this._refresh();
            return Clutter.EVENT_STOP;
        }

        return Clutter.EVENT_PROPAGATE;
    }

    _menuTextEntryFocused() {
        let focused = global.stage.get_key_focus();
        while (focused) {
            for (const card of this._cards.values()) {
                if (Object.values(card._entries || {}).includes(focused))
                    return true;
                if (Object.values(card._entries || {}).some(entry => entry.clutter_text === focused))
                    return true;
            }
            focused = focused.get_parent();
        }
        return false;
    }

    _setView(view, focusButton = false) {
        if (this._view === view) {
            if (focusButton)
                this._viewButtons.get(view)?.button.grab_key_focus();
            return;
        }
        this._view = view;
        this._syncMenu();
        if (focusButton)
            this._viewButtons.get(view)?.button.grab_key_focus();
    }

    _rebuild() {
        this._stopCardDots();
        this.menu.removeAll();
        this.menu.box.add_style_class_name('agent-mission-control-menu');
        this._cards = new Map();
        this._cardOrder = [];
        this._sectionInfo = null;
        this._sectionAction = null;
        this._sectionActionAction = null;

        const header = new PopupMenu.PopupBaseMenuItem({
            reactive: false,
            can_focus: false,
            style_class: 'agent-mission-control-header',
        });
        const heading = new St.BoxLayout({...VERTICAL_LAYOUT, x_expand: true});
        heading.add_child(new St.Label({
            text: 'Agent Mission Control',
            style_class: 'agent-mission-control-header-title',
        }));
        this._menuSummary = new St.Label({
            text: !this._loaded ? 'connecting…'
                : this._reachable ? model.summary(this._workflows) : this._down.summary,
            style_class: `agent-mission-control-header-summary${this._reachable ? '' : ' agent-mission-control-dim'}`,
        });
        heading.add_child(this._menuSummary);
        header.add_child(heading);
        this.menu.addMenuItem(header);

        const viewRow = new PopupMenu.PopupBaseMenuItem({
            reactive: false,
            can_focus: false,
            style_class: 'agent-mission-control-view-row',
        });
        const controls = new St.BoxLayout({...VERTICAL_LAYOUT, x_expand: true});
        const views = new St.BoxLayout({x_expand: true});
        this._viewButtons = new Map();
        for (const [view, title] of [
            [model.VIEW_ALL, 'All'],
            [model.VIEW_NEEDS_YOU, 'Needs you'],
            [model.VIEW_WAITING, 'Waiting'],
            [model.VIEW_RUNNING, 'Running'],
        ]) {
            const label = new St.Label({text: title});
            const button = new St.Button({
                can_focus: true,
                x_expand: true,
                style_class: 'agent-mission-control-view',
                child: label,
            });
            button.connect('clicked', () => {
                this._setView(view);
            });
            views.add_child(button);
            this._viewButtons.set(view, {button, label, title});
        }
        controls.add_child(views);
        controls.add_child(new St.Label({
            text: 'Ctrl+1–4: switch view · Ctrl+R: refresh',
            style_class: 'agent-mission-control-shortcut-hint',
        }));
        viewRow.add_child(controls);
        this.menu.addMenuItem(viewRow);

        this._workflowSection = new PopupMenu.PopupMenuSection();
        this.menu.addMenuItem(this._workflowSection);
        this._syncMenu();
    }

    // _syncMenu updates the open menu in place: summary, view counts and
    // selection, and the workflow cards (created, re-rendered only when their
    // signature changed, re-ordered by urgency, hidden by the current view).
    _syncMenu() {
        if (!this._menuSummary || !this._workflowSection)
            return;

        this._menuSummary.text = !this._loaded ? 'connecting…'
            : this._reachable ? model.summary(this._workflows) : this._down.summary;
        if (this._reachable)
            this._menuSummary.remove_style_class_name('agent-mission-control-dim');
        else
            this._menuSummary.add_style_class_name('agent-mission-control-dim');

        const counts = model.viewCounts(this._workflows);
        for (const [view, buttonInfo] of this._viewButtons) {
            const count = view === model.VIEW_ALL ? counts.all
                : view === model.VIEW_NEEDS_YOU ? counts.needsYou
                    : view === model.VIEW_WAITING ? counts.waiting : counts.running;
            buttonInfo.label.text = `${buttonInfo.title} ${count}`;
            if (this._view === view)
                buttonInfo.button.add_style_class_name('agent-mission-control-view-selected');
            else
                buttonInfo.button.remove_style_class_name('agent-mission-control-view-selected');
        }

        if (this._loaded && !this._reachable) {
            this._hideWorkflowCards();
            this._setSectionInfo(this._down.hint);
            this._setSectionAction(this._down.action);
            return;
        }
        if (!this._reachable) {
            this._hideWorkflowCards();
            this._setSectionInfo('');
            this._setSectionAction(null);
            return;
        }
        this._setSectionAction(null);

        const workflows = model.filterWorkflows(this._workflows, this._view);
        const visible = new Set(workflows.map(w => this._workflowKey(w)));
        const present = new Set(this._workflows.map(w => this._workflowKey(w)));

        for (const [id, card] of this._cards) {
            if (present.has(id))
                continue;
            this._removeWorkflowCard(card);
        }

        for (const w of this._workflows) {
            const id = this._workflowKey(w);
            let card = this._cards.get(id);
            try {
                const oldRank = card ? model.urgencyRank(card._workflow) : null;
                if (!card) {
                    card = this._workflowItem(w);
                    this._cards.set(id, card);
                    this._insertWorkflowCard(card);
                } else if (card._missing || card._signature !== this._workflowCardSignature(w)) {
                    this._workflowItem(w, card);
                } else {
                    card._workflow = w;
                    card._statusLabel.text = `${model.statusLabel(w)} · ${model.age(w.updated_at)}`;
                }
                if (oldRank !== null && oldRank !== model.urgencyRank(card._workflow))
                    this._reorderWorkflowCard(card);
                card._missing = false;
                card.visible = visible.has(id);
                this._setCardDotsActive(card, card.visible && w.status === 'running' && !w.stale);
            } catch (e) {
                logError(e, `agent-mission-control: couldn't render workflow ${model.clean(w.title || w.source_id, 80)}`);
                if (!card)
                    this._setSectionInfo(`Couldn't display ${model.clean(w.title || w.source_id, 80)}. See the shell log for details.`);
            }
        }

        if (!this._workflows.length)
            this._setSectionInfo('No workflows yet: start an opencode or claude session.');
        else if (!workflows.length)
            this._setSectionInfo(`No ${this._view === model.VIEW_NEEDS_YOU ? 'workflows need you'
                : this._view === model.VIEW_WAITING ? 'workflows are waiting'
                    : 'workflows are running'} right now.`);
        else
            this._setSectionInfo('');
    }

    _workflowKey(w) {
        return w.id || w.source_id;
    }

    _workflowCardSignature(w) {
        return JSON.stringify([
            w.id, w.source_id, w.title, w.location, w.status, w.message,
            w.stale, w.stale_reason, w.pids, w.pending, w.capabilities,
        ]);
    }

    _insertWorkflowCard(card) {
        const id = card._workflowId;
        const rank = model.urgencyRank(card._workflow);
        let position = this._cardOrder.findIndex(existingId =>
            model.urgencyRank(this._cards.get(existingId)._workflow) > rank);
        if (position < 0)
            position = this._cardOrder.length;
        this._workflowSection.addMenuItem(card, position);
        this._cardOrder.splice(position, 0, id);
    }

    _reorderWorkflowCard(card) {
        const current = this._cardOrder.indexOf(card._workflowId);
        if (current < 0)
            return;
        this._cardOrder.splice(current, 1);
        const rank = model.urgencyRank(card._workflow);
        let position = this._cardOrder.findIndex(existingId =>
            model.urgencyRank(this._cards.get(existingId)._workflow) > rank);
        if (position < 0)
            position = this._cardOrder.length;
        this._workflowSection.moveMenuItem(card, position);
        this._cardOrder.splice(position, 0, card._workflowId);
    }

    _removeWorkflowCard(card) {
        this._setCardDotsActive(card, false);
        card.visible = false;
        card._missing = true;
        this._cardState.delete(card._workflowId);
    }

    _hideWorkflowCards() {
        for (const card of this._cards.values()) {
            card.visible = false;
            this._setCardDotsActive(card, false);
        }
    }

    _setSectionInfo(text) {
        if (!this._sectionInfo) {
            this._sectionInfo = new PopupMenu.PopupMenuItem('', {reactive: false});
            this._sectionInfo.label.clutter_text.line_wrap = true;
            this._workflowSection.addMenuItem(this._sectionInfo);
        }
        this._sectionInfo.visible = !!text;
        if (text)
            this._sectionInfo.label.text = model.clean(text, 400);
    }

    // The daemon-down menu's one button, if the state offers one.
    _setSectionAction(action) {
        if (this._sectionAction && JSON.stringify(action) === JSON.stringify(this._sectionActionAction))
            return;
        if (this._sectionAction) {
            this._sectionAction.destroy();
            this._sectionAction = null;
            this._sectionActionAction = null;
        }
        if (!action)
            return;
        const item = new PopupMenu.PopupMenuItem(action.label);
        item.connect('activate', () => this._runAction(action));
        this._workflowSection.addMenuItem(item);
        this._sectionAction = item;
        this._sectionActionAction = action;
    }

    // The daemon-down menu's one button. Fixed argv, never a shell string.
    _runAction(action) {
        try {
            if (action.kind === 'start' || action.kind === 'restart') {
                const proc = Gio.Subprocess.new(['systemctl', '--user', action.kind, model.SERVICE],
                    Gio.SubprocessFlags.STDERR_PIPE);
                proc.communicate_utf8_async(null, null, (p, res) => {
                    try {
                        const [, , stderr] = p.communicate_utf8_finish(res);
                        if (!p.get_successful())
                            Main.notify('Agent Mission Control', model.clean(stderr || `systemctl ${action.kind} failed`));
                    } catch (e) {
                        logError(e, 'agent-mission-control');
                    }
                    this._refresh();
                });
            } else if (action.kind === 'copy') {
                St.Clipboard.get_default().set_text(St.ClipboardType.CLIPBOARD, action.arg);
                Main.notify('Agent Mission Control', `Copied: ${action.arg}`);
            } else if (action.kind === 'open') {
                Gio.AppInfo.launch_default_for_uri(action.arg, null);
            }
        } catch (e) {
            logError(e, 'agent-mission-control');
            Main.notify('Agent Mission Control', model.clean(e.message));
        }
    }

    _workflowItem(w, card = null) {
        if (!card) {
            card = new PopupMenu.PopupBaseMenuItem({
                reactive: false,
                can_focus: false,
                style_class: 'agent-mission-control-card',
            });
            card._workflowId = this._workflowKey(w);
            card._restoreEntry = null;
        } else {
            if (!card._missing)
                this._captureCardInput(card);
            this._setCardDotsActive(card, false);
            if (card._content) {
                card.remove_child(card._content);
                card._content.destroy();
            }
        }
        card._workflow = w;
        card._entries = {};
        card._dots = null;
        const content = new St.BoxLayout({
            ...VERTICAL_LAYOUT,
            x_expand: true,
            style_class: 'agent-mission-control-card-content',
        });
        card._content = content;
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
            card._dots = dots;
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
        card._statusLabel = status;
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
        else
            card.remove_style_class_name('agent-mission-control-card-attention');
        card.opacity = w.stale ? 150 : 255;

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
            content.add_child(this._entry('or type an answer, Enter to send', text => this._answer(w, ask.id, text), w, `answer:${ask.id}`, card));
        else if (model.canFollowUp(w)) {
            const hint = w.status === 'done' ? 'follow up…'
                : w.status === 'needs_input' ? 'reply…' : 'tell it how to continue…';
            content.add_child(this._entry(hint, text => this._send(w, text), w, 'follow-up', card));
        }

        if (!question && ask && !several && !w.stale) {
            const questions = (ask.questions || []).map(q => `• ${model.clean(q.question, 300)}`).join('\n');
            this._cardLabel(content, `${questions}\nAnswer in its terminal${w.pids && w.pids.length ? ' (terminal button jumps there)' : ''}.`, 'agent-mission-control-card-secondary');
        } else if ((!canInput || several) && !w.stale && (ask || w.status === 'needs_input') && !several) {
            this._cardLabel(content, `Answer in its terminal${w.pids && w.pids.length ? ' (terminal button jumps there)' : ''}.`, 'agent-mission-control-card-secondary');
        }

        card._signature = this._workflowCardSignature(w);
        const restoreEntry = card._restoreEntry;
        card._restoreEntry = null;
        if (restoreEntry && card._entries[restoreEntry]) {
            const entry = card._entries[restoreEntry];
            GLib.idle_add(GLib.PRIORITY_DEFAULT_IDLE, () => {
                if (this.menu.isOpen && entry.get_parent())
                    entry.grab_key_focus();
                return GLib.SOURCE_REMOVE;
            });
        }
        return card;
    }

    // Typed text, cursors and picked options, keyed per workflow, so a card
    // re-render (state change) or menu reopen doesn't lose them.
    _cardStateFor(w) {
        const id = this._workflowKey(w);
        if (!this._cardState.has(id))
            this._cardState.set(id, {entries: {}, cursors: {}, picked: {}});
        return this._cardState.get(id);
    }

    _captureCardInput(card) {
        const state = this._cardStateFor(card._workflow);
        const focused = global.stage.get_key_focus();
        card._restoreEntry = null;
        for (const [key, entry] of Object.entries(card._entries || {})) {
            state.entries[key] = entry.get_text();
            state.cursors[key] = entry.clutter_text.cursor_position;
            if (focused === entry || focused === entry.clutter_text)
                card._restoreEntry = key;
        }
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

    _setCardDotsActive(card, active) {
        if (!card._dots)
            return;
        this._runningDots = this._runningDots.filter(label => label !== card._dots);
        if (active)
            this._animateCardDots(card._dots);
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

        const state = this._cardStateFor(w);
        let picked = (state.picked[askId] || []).filter(option => options.includes(option));
        state.picked[askId] = picked;
        const send = this._button('Send selected', 'agent-mission-control-choice agent-mission-control-send', () => {
            this._answer(w, askId, picked);
            this.menu.close();
        });
        send.reactive = false;
        send.can_focus = false;
        send.opacity = 128;
        for (let i = 0; i < options.length; i++) {
            const opt = options[i];
            const selected = picked.includes(opt);
            const label = new St.Label({text: `${selected ? '✓ ' : ''}${cleanOptions[i]}`});
            const button = new St.Button({
                can_focus: true,
                style_class: `agent-mission-control-choice${permission && opt === 'reject' ? ' agent-mission-control-choice-reject' : ''}${selected ? ' agent-mission-control-choice-selected' : ''}`,
                child: label,
            });
            button.connect('clicked', () => {
                picked = model.togglePick(picked, opt, options);
                state.picked[askId] = picked;
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
        if (picked.length) {
            send.reactive = true;
            send.can_focus = true;
            send.opacity = 255;
        }
        row.add_child(send);
    }

    _entry(hint, onSubmit, w, entryKey, card) {
        const entry = new St.Entry({
            hint_text: hint,
            can_focus: true,
            x_expand: true,
            style_class: 'agent-mission-control-entry',
        });
        const state = this._cardStateFor(w);
        entry.set_text(state.entries[entryKey] || '');
        if (Number.isInteger(state.cursors[entryKey]))
            entry.clutter_text.cursor_position = state.cursors[entryKey];
        card._entries[entryKey] = entry;
        entry.clutter_text.connect('text-changed', () => {
            state.entries[entryKey] = entry.get_text();
            state.cursors[entryKey] = entry.clutter_text.cursor_position;
        });
        entry.clutter_text.connect('activate', () => {
            const text = entry.get_text().trim();
            if (text) {
                entry.set_text('');
                state.entries[entryKey] = '';
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
        this._settings = this.getSettings();
        const action = Main.wm.addKeybinding(
            'toggle-menu',
            this._settings,
            Meta.KeyBindingFlags.IGNORE_AUTOREPEAT,
            Shell.ActionMode.NORMAL | Shell.ActionMode.POPUP,
            () => this._indicator?.menu.toggle());
        if (action === Meta.KeyBindingAction.NONE && this._settings.get_strv('toggle-menu').length)
            log('agent-mission-control: the global shortcut could not be registered; it may already be in use');
    }

    disable() {
        Main.wm.removeKeybinding('toggle-menu');
        this._settings = null;
        this._indicator?.destroy();
        this._indicator = null;
    }
}
