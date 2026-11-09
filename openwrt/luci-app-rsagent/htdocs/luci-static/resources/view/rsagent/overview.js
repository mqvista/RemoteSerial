'use strict';
'require view';
'require rpc';
'require ui';
'require poll';
'require dom';

// RemoteSerial (rsagent) in LuCI: service control, Tailscale setup, bridge on/off, console port
// and LAN access, and a one-click way into the full console. Everything goes through the rpcd
// plugin luci.rsagent, which calls "rsagent ctl"; rsagent's own config stays the only source of truth.

var callStatus = rpc.declare({ object: 'luci.rsagent', method: 'status' });
var callService = rpc.declare({ object: 'luci.rsagent', method: 'service', params: [ 'action' ] });
var callBridge = rpc.declare({ object: 'luci.rsagent', method: 'bridge', params: [ 'id', 'action' ] });
var callConsole = rpc.declare({ object: 'luci.rsagent', method: 'console', params: [ 'port', 'lan' ] });
var callToken = rpc.declare({ object: 'luci.rsagent', method: 'token' });
var callTailscale = rpc.declare({ object: 'luci.rsagent', method: 'tailscale', params: [ 'action' ] });
var callTailscaleJob = rpc.declare({ object: 'luci.rsagent', method: 'tailscale_job' });
var callTailscaleLogin = rpc.declare({ object: 'luci.rsagent', method: 'tailscale_login' });
var callAuthKey = rpc.declare({ object: 'luci.rsagent', method: 'tailscale_authkey', params: [ 'authKey', 'tags' ] });
var callBridgeAdd = rpc.declare({ object: 'luci.rsagent', method: 'bridge_add', params: [ 'device', 'name', 'baud', 'mode', 'tcpPort', 'matchUSB' ] });
var callBridgeRemove = rpc.declare({ object: 'luci.rsagent', method: 'bridge_remove', params: [ 'id' ] });

var BAUDS = [ 1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200, 230400, 460800, 921600 ];

function isTailnetHost(h) {
	return /^100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\./.test(h) || /^\[?fd7a:115c:a1e0:/i.test(h) || /\.ts\.net$/i.test(h);
}

// Every rpcd reply is an object; failures come back as {error: "..."}.
function checked(res) {
	if (res && res.error)
		throw new Error(res.error);
	return res;
}

function notifyError(err) {
	ui.addNotification(null, E('p', {}, [ _('Error'), ': ', String(err && err.message || err) ]), 'danger');
}

function bytes(n) {
	return '%1024.1mB'.format(n || 0);
}

function stateLabel(state) {
	switch (state) {
	case 'Running': return _('Connected');
	case 'NeedsLogin': return _('Needs login');
	case 'NeedsMachineAuth': return _('Waiting for approval');
	case 'Starting': return _('Starting');
	case 'Stopped': return _('Stopped');
	case 'NotInstalled': return _('Not installed');
	case 'NotRunning': return _('Not running');
	case 'NotConfigured': return _('Not logged in');
	case 'Error': return _('Error');
	default: return state || '-';
	}
}

function bridgeStateLabel(b) {
	if (!b.enabled) return _('Disabled');
	switch (b.state) {
	case 'Connected': return _('Client connected');
	case 'Listening': return _('Waiting for client');
	case 'WaitingNetwork': return _('Waiting for network');
	case 'WaitingDevice': return _('Waiting for device');
	case 'Error': return _('Error');
	default: return b.state || '-';
	}
}

// E() puts a lone string child in through innerHTML; arrays of strings become text nodes.
// Wrap every value that comes from rsagent (bridge names, errors, device info) with txt().
function txt(s) {
	return [ s == null ? '' : String(s) ];
}

function badge(text, ok) {
	return E('span', { 'class': 'label', 'style': 'background:' + (ok ? '#4caf50' : '#9e9e9e') + ';color:#fff;padding:1px 6px;border-radius:3px' }, txt(text));
}

function row(label, value) {
	return E('tr', { 'class': 'tr' }, [
		E('td', { 'class': 'td left', 'width': '33%' }, label),
		E('td', { 'class': 'td left' }, typeof value === 'string' ? txt(value) : value)
	]);
}

return view.extend({
	load: function() {
		return callStatus().catch(function(e) { return { error: String(e) }; });
	},

	// ---------------------------------------------------------------- Tailscale setup
	renderTailscale: function(st) {
		var ts = st.status && st.status.tailscale;
		if (!st.running || !ts || ts.state === 'Running')
			return E('div');

		var self = this, sys = ts.system || {}, body = [], buttons = [];
		var detail = ts.message ? E('p', { 'style': 'opacity:.8' }, txt(ts.message)) : '';

		switch (ts.state) {
		case 'NotInstalled':
			body.push(E('p', {}, _('RemoteSerial uses the router\'s own Tailscale (the "tailscale" package), which is not installed yet.')));
			if (sys.canManage)
				buttons.push(E('button', { 'class': 'cbi-button cbi-button-action', 'click': ui.createHandlerFn(this, 'runTailscale', 'install') }, _('Install Tailscale')));
			if (sys.installCommand)
				body.push(E('p', {}, [ _('Or run on the router:'), ' ', E('code', {}, txt(sys.installCommand)) ]));
			if (sys.needMB && sys.freeMB != null && sys.freeMB < sys.needMB)
				body.push(E('p', { 'class': 'alert-message warning' }, _('Only %d MB free on the overlay; about %d MB is needed.').format(sys.freeMB, sys.needMB)));
			break;
		case 'NotRunning':
			body.push(E('p', {}, _('Tailscale is installed but not running.')));
			buttons.push(E('button', { 'class': 'cbi-button cbi-button-action', 'click': ui.createHandlerFn(this, 'runTailscale', 'start') }, _('Start Tailscale and enable at boot')));
			break;
		case 'NeedsLogin':
		case 'NotConfigured':
			// rsagent's message says the same thing, or carries the reason (e.g. an invalid key): show one of them
			body.push(E('p', {}, txt(ts.message || _('Tailscale is running but this router is not logged in to a tailnet yet.'))));
			detail = '';
			if (ts.authURL)
				body.push(E('p', {}, [ _('Open this link to log in:'), ' ', E('a', { 'href': ts.authURL, 'target': '_blank', 'rel': 'noreferrer' }, txt(ts.authURL)) ]));
			else
				body.push(E('p', {}, E('button', { 'class': 'cbi-button cbi-button-action', 'click': ui.createHandlerFn(this, 'loginTailscale') }, _('Log in in browser'))));
			body.push(this.renderAuthKey(ts));
			break;
		case 'NeedsMachineAuth':
			body.push(E('p', {}, _('Logged in. An admin must approve this device in the Tailscale admin console.')));
			break;
		default:
			body.push(E('p', {}, _('Tailscale is not ready.')));
		}

		var log = E('pre', { 'id': 'rsagent-job', 'style': 'display:none;max-height:240px;overflow:auto;white-space:pre-wrap' });
		return E('div', { 'class': 'cbi-section' }, [
			E('h3', {}, _('Set up Tailscale') + ' — ' + stateLabel(ts.state)),
			detail,
			E('div', {}, body),
			buttons.length ? E('p', {}, buttons) : '',
			log
		]);
	},

	// The inputs are created once in render() and reused, so the 5 s refresh does not wipe what was typed.
	renderAuthKey: function(ts) {
		if (!this.keyInput.value && !this.tagsInput.value && ts.advertiseTags && ts.advertiseTags.length)
			this.tagsInput.value = ts.advertiseTags.join(',');
		return E('div', { 'style': 'margin-top:10px' }, [
			E('p', {}, _('Or log in with an auth key:')),
			E('div', { 'class': 'cbi-value' }, [
				E('label', { 'class': 'cbi-value-title' }, _('Auth key')),
				E('div', { 'class': 'cbi-value-field' }, [ this.keyInput,
					E('div', { 'class': 'cbi-value-description' }, _('An auth key (tskey-auth-…) or an OAuth client secret (tskey-client-…) from the Tailscale admin console. It is used once and not stored.')) ])
			]),
			E('div', { 'class': 'cbi-value' }, [
				E('label', { 'class': 'cbi-value-title' }, _('Tags')),
				E('div', { 'class': 'cbi-value-field' }, [ this.tagsInput,
					E('div', { 'class': 'cbi-value-description' }, _('Comma separated, e.g. tag:serial-agent. Required for an OAuth client secret; leave empty for a personal auth key.')) ])
			]),
			E('div', { 'class': 'cbi-value' }, [
				E('label', { 'class': 'cbi-value-title' }, ''),
				E('div', { 'class': 'cbi-value-field' }, E('button', { 'class': 'cbi-button cbi-button-apply', 'click': ui.createHandlerFn(this, 'loginAuthKey') }, _('Log in with auth key')))
			])
		]);
	},

	loginAuthKey: function() {
		var self = this, key = this.keyInput.value.trim(), tags = this.tagsInput.value.trim();
		if (!key) {
			notifyError(_('Enter an auth key first.'));
			return;
		}
		if (/^tskey-client-/.test(key) && !tags) {
			notifyError(_('An OAuth client secret needs at least one tag, e.g. tag:serial-agent.'));
			return;
		}
		return callAuthKey(key, tags).then(checked).then(function() {
			self.keyInput.value = '';
			ui.addNotification(null, E('p', {}, _('Logging in… the state updates in a few seconds.')), 'info');
			return self.refresh();
		}).catch(notifyError);
	},

	runTailscale: function(action) {
		var self = this;
		return callTailscale(action).then(checked).then(function() {
			return self.followJob();
		}).catch(notifyError);
	},

	followJob: function() {
		var self = this;
		return callTailscaleJob().then(checked).then(function(job) {
			var pre = document.getElementById('rsagent-job');
			if (pre) {
				pre.style.display = '';
				pre.textContent = job.log || '';
				pre.scrollTop = pre.scrollHeight;
			}
			if (job.running)
				return new Promise(function(resolve) { window.setTimeout(resolve, 1500); }).then(function() { return self.followJob(); });
			if (job.error)
				notifyError(job.error);
			return self.refresh();
		});
	},

	loginTailscale: function() {
		var self = this;
		return callTailscaleLogin().then(checked).then(function(snap) {
			if (snap.authURL)
				window.open(snap.authURL, '_blank', 'noreferrer');
			return self.refresh();
		}).catch(notifyError);
	},

	// ---------------------------------------------------------------- service and status
	renderStatus: function(st) {
		var svc = st.service || {}, s = st.status || {}, ts = s.tailscale || {}, con = st.console || {};
		var running = !!st.running;

		var svcButtons = [];
		if (svc.installed) {
			if (running) {
				svcButtons.push(E('button', { 'class': 'cbi-button cbi-button-reset', 'click': ui.createHandlerFn(this, 'service', 'stop') }, _('Stop')), ' ');
				svcButtons.push(E('button', { 'class': 'cbi-button cbi-button-reload', 'click': ui.createHandlerFn(this, 'service', 'restart') }, _('Restart')), ' ');
			} else {
				svcButtons.push(E('button', { 'class': 'cbi-button cbi-button-apply', 'click': ui.createHandlerFn(this, 'service', 'start') }, _('Start')), ' ');
			}
			svcButtons.push(svc.enabled
				? E('button', { 'class': 'cbi-button', 'click': ui.createHandlerFn(this, 'service', 'disable') }, _('Disable start at boot'))
				: E('button', { 'class': 'cbi-button', 'click': ui.createHandlerFn(this, 'service', 'enable') }, _('Enable start at boot')));
		}

		var svcText = svc.installed
			? [ badge(running ? _('Running') : _('Stopped'), running), ' ', svc.enabled ? _('starts at boot') : _('does not start at boot') ]
			: [ badge(_('Not installed'), false), ' ', E('code', {}, 'rsagent service install') ];

		var tsText = '-';
		if (running) {
			tsText = [ badge(stateLabel(ts.state), ts.state === 'Running') ];
			if (ts.ip) tsText.push(' ', E('code', {}, txt(ts.ip)));
			if (ts.dnsName) tsText.push(' ', ts.dnsName.replace(/\.$/, ''));
			if (ts.backend === 'embedded') tsText.push(' ', E('em', {}, _('(built-in Tailscale)')));
		}

		var rows = [
			row(_('Service'), E('div', {}, svcText.concat([ E('div', { 'style': 'margin-top:6px' }, svcButtons) ]))),
			row(_('Version'), running ? (s.version || '-') : '-'),
			row(_('Tailscale'), E('div', {}, tsText)),
			row(_('Console'), E('div', {}, this.renderOpen(st)))
		];
		if (st.error)
			rows.push(row(_('Error'), st.error));

		return E('div', { 'class': 'cbi-section' }, [
			E('h3', {}, _('Status')),
			E('table', { 'class': 'table' }, rows)
		]);
	},

	renderOpen: function(st) {
		var con = st.console || {}, ts = (st.status && st.status.tailscale) || {};
		var host = window.location.hostname, viaTailnet = isTailnetHost(host);
		var out = [];
		if (!st.running) {
			out.push(E('span', { 'style': 'opacity:.8' }, _('Start the service to open the console.')));
			return out;
		}
		var canOpen = viaTailnet || con.lan;
		out.push(E('button', {
			'class': 'cbi-button cbi-button-action',
			'disabled': canOpen ? null : '',
			'click': ui.createHandlerFn(this, 'openConsole', con.port, viaTailnet ? (con.webPort || con.port) : con.port)
		}, _('Open console')));
		out.push(' ', E('span', { 'style': 'opacity:.8' }, _('port %d').format(con.port)));
		if (!canOpen)
			out.push(E('p', { 'style': 'opacity:.8;margin-top:6px' }, _('Turn on "Allow LAN access" below to open the console from this browser.')));
		if (ts.ip && ts.state === 'Running')
			out.push(E('p', { 'style': 'opacity:.8;margin-top:6px' }, [ _('From the tailnet:'), ' ', E('code', {}, 'http://' + ts.ip + ':' + (con.webPort || con.port) + '/') ]));
		return out;
	},

	openConsole: function(lanPort, port) {
		// Open the window right away so the popup blocker allows it, then point it at the console.
		var win = window.open('about:blank', '_blank');
		return callToken().then(checked).then(function(t) {
			var host = window.location.hostname;
			var url = 'http://' + host + ':' + (t.port === lanPort ? port : t.port) + '/?' + encodeURIComponent(t.param || 'rsagent_token') + '=' + encodeURIComponent(t.token);
			if (win)
				win.location.href = url;
			else
				window.location.href = url;
		}).catch(function(e) {
			if (win) win.close();
			notifyError(e);
		});
	},

	service: function(action) {
		var self = this;
		return callService(action).then(checked).then(function() {
			// procd needs a moment before the API answers again
			return new Promise(function(resolve) { window.setTimeout(resolve, action === 'stop' ? 300 : 1500); });
		}).then(function() { return self.refresh(); }).catch(notifyError);
	},

	// ---------------------------------------------------------------- bridges
	renderBridges: function(st) {
		var bridges = (st.status && st.status.bridges) || [];
		var self = this;
		if (!st.running)
			return E('div');

		var rows = [ E('tr', { 'class': 'tr table-titles' }, [
			E('th', { 'class': 'th' }, _('Name')),
			E('th', { 'class': 'th' }, _('Serial port')),
			E('th', { 'class': 'th' }, _('TCP port')),
			E('th', { 'class': 'th' }, _('State')),
			E('th', { 'class': 'th' }, _('Client')),
			E('th', { 'class': 'th' }, _('Serial → TCP / TCP → serial')),
			E('th', { 'class': 'th cbi-section-actions' }, '')
		]) ];
		bridges.forEach(function(b) {
			rows.push(E('tr', { 'class': 'tr' }, [
				E('td', { 'class': 'td' }, txt(b.name || b.id)),
				E('td', { 'class': 'td' }, txt(b.resolvedPort || b.port || '-')),
				E('td', { 'class': 'td' }, String(b.tcpPort)),
				E('td', { 'class': 'td' }, [ badge(bridgeStateLabel(b), b.enabled && b.state === 'Connected'), b.lastError ? E('div', { 'style': 'opacity:.8' }, txt(b.lastError)) : '' ]),
				E('td', { 'class': 'td' }, txt(b.client || '-')),
				E('td', { 'class': 'td' }, bytes(b.fromDevice) + ' / ' + bytes(b.toDevice)),
				E('td', { 'class': 'td cbi-section-actions', 'style': 'white-space:nowrap' }, [
					E('button', {
						'class': 'cbi-button ' + (b.enabled ? 'cbi-button-reset' : 'cbi-button-apply'),
						'click': ui.createHandlerFn(self, 'bridge', b.id, b.enabled ? 'stop' : 'start')
					}, b.enabled ? _('Disable') : _('Enable')), ' ',
					E('button', { 'class': 'cbi-button cbi-button-remove', 'click': ui.createHandlerFn(self, 'removeBridge', b) }, _('Delete'))
				])
			]));
		});
		if (!bridges.length)
			rows.push(E('tr', { 'class': 'tr placeholder' }, E('td', { 'class': 'td', 'colspan': 7 }, E('em', {}, _('No bridges yet. Add one from a serial device below.')))));

		return E('div', { 'class': 'cbi-section' }, [
			E('h3', {}, _('Bridges')),
			E('table', { 'class': 'table' }, rows)
		]);
	},

	removeBridge: function(b) {
		var self = this;
		if (!window.confirm(_('Delete bridge "%s"? Its TCP port %d stops listening.').format(b.name || b.id, b.tcpPort)))
			return;
		return callBridgeRemove(b.id).then(checked).then(function() { return self.refresh(); }).catch(notifyError);
	},

	// ---------------------------------------------------------------- serial devices
	renderPorts: function(st) {
		if (!st.running)
			return E('div');
		var ports = st.ports || [], bridges = (st.status && st.status.bridges) || [], self = this;
		var byId = {};
		bridges.forEach(function(b) { byId[b.id] = b; });

		var rows = [ E('tr', { 'class': 'tr table-titles' }, [
			E('th', { 'class': 'th' }, _('Device')),
			E('th', { 'class': 'th' }, _('Type')),
			E('th', { 'class': 'th' }, _('USB ID')),
			E('th', { 'class': 'th' }, _('Serial number')),
			E('th', { 'class': 'th' }, _('Used by')),
			E('th', { 'class': 'th cbi-section-actions' }, '')
		]) ];
		ports.forEach(function(p) {
			var used = p.usedBy ? (byId[p.usedBy] ? (byId[p.usedBy].name || p.usedBy) : p.usedBy) : '';
			rows.push(E('tr', { 'class': 'tr' }, [
				E('td', { 'class': 'td' }, E('code', {}, txt(p.name))),
				E('td', { 'class': 'td' }, txt(p.isUSB ? (p.chip || _('USB serial')) : _('Built-in'))),
				E('td', { 'class': 'td' }, txt(p.vid ? p.vid + ':' + p.pid : '-')),
				E('td', { 'class': 'td' }, txt(p.serialNumber || '-')),
				E('td', { 'class': 'td' }, txt(used || '-')),
				E('td', { 'class': 'td cbi-section-actions' }, E('button', {
					'class': 'cbi-button cbi-button-add',
					'disabled': used ? '' : null,
					'click': ui.createHandlerFn(self, 'showAddBridge', p, st)
				}, _('Add bridge')))
			]));
		});
		if (!ports.length)
			rows.push(E('tr', { 'class': 'tr placeholder' }, E('td', { 'class': 'td', 'colspan': 6 }, E('em', {},
				_('No serial devices found. Plug in a USB serial adapter; it needs a driver such as kmod-usb-serial-ch341, -cp210x, -ftdi, -pl2303 or kmod-usb-acm.')))));

		return E('div', { 'class': 'cbi-section' }, [
			E('h3', {}, _('Serial devices')),
			E('table', { 'class': 'table' }, rows)
		]);
	},

	showAddBridge: function(p, st) {
		var self = this;
		var used = {};
		((st.status && st.status.bridges) || []).forEach(function(b) { used[b.tcpPort] = true; });
		var next = 2217;
		while (used[next] || next === (st.console || {}).port) next++;

		var name = E('input', { 'class': 'cbi-input-text', 'value': p.name.replace(/^\/dev\//, ''), 'style': 'width:100%' });
		var baud = E('select', { 'class': 'cbi-input-select' }, BAUDS.map(function(v) {
			return E('option', { 'value': v, 'selected': v === 115200 ? '' : null }, String(v));
		}));
		var mode = E('select', { 'class': 'cbi-input-select' }, [
			E('option', { 'value': 'rfc2217', 'selected': '' }, _('RFC2217 (client can change baud rate, recommended)')),
			E('option', { 'value': 'raw' }, _('Raw TCP (fixed parameters)'))
		]);
		var tcp = E('input', { 'type': 'number', 'class': 'cbi-input-text', 'min': 1, 'max': 65535, 'value': next, 'style': 'width:8em' });
		var match = E('input', { 'type': 'checkbox', 'checked': p.isUSB && (p.vid || p.serialNumber) ? '' : null, 'disabled': p.isUSB && (p.vid || p.serialNumber) ? null : '' });

		function field(label, input, descr) {
			return E('div', { 'class': 'cbi-value' }, [
				E('label', { 'class': 'cbi-value-title' }, label),
				E('div', { 'class': 'cbi-value-field' }, [ input, descr ? E('div', { 'class': 'cbi-value-description' }, descr) : '' ])
			]);
		}

		ui.showModal(_('Add bridge for %s').format('%h'.format(p.name)), [
			field(_('Name'), name),
			field(_('Baud rate'), baud),
			field(_('Mode'), mode),
			field(_('TCP port'), tcp, _('Clients connect to this port on the router\'s Tailscale IP.')),
			field(_('Match by USB ID'), match, _('Find the adapter by its USB ID and serial number, so the bridge keeps working when the device name changes (e.g. ttyUSB0 → ttyUSB1).')),
			E('div', { 'class': 'right' }, [
				E('button', { 'class': 'cbi-button', 'click': ui.hideModal }, _('Cancel')), ' ',
				E('button', { 'class': 'cbi-button cbi-button-positive', 'click': ui.createHandlerFn(this, function() {
					var port = parseInt(tcp.value, 10);
					if (!(port >= 1 && port <= 65535)) {
						notifyError(_('The port must be between 1 and 65535.'));
						return;
					}
					return callBridgeAdd(p.name, name.value.trim(), parseInt(baud.value, 10), mode.value, port, match.checked).then(checked).then(function() {
						ui.hideModal();
						return self.refresh();
					}).catch(notifyError);
				}) }, _('Add'))
			])
		]);
	},

	bridge: function(id, action) {
		var self = this;
		return callBridge(id, action).then(checked).then(function(r) {
			if (r && r.warning)
				ui.addNotification(null, E('p', {}, txt(r.warning)), 'warning');
			return self.refresh();
		}).catch(notifyError);
	},

	// ---------------------------------------------------------------- console settings
	renderSettings: function(st) {
		var con = st.console || {};
		var port = E('input', { 'type': 'number', 'class': 'cbi-input-text', 'min': 1, 'max': 65535, 'value': con.port || 7890, 'style': 'width:8em' });
		var lan = E('input', { 'type': 'checkbox', 'checked': con.lan ? '' : null });
		var self = this;

		return E('div', { 'class': 'cbi-section' }, [
			E('h3', {}, _('Console access')),
			E('div', { 'class': 'cbi-section-descr' }, _('The console is always reachable on the router itself and from the tailnet. Changes apply immediately: rsagent opens the new port first and only then closes the old one.')),
			E('div', { 'class': 'cbi-value' }, [
				E('label', { 'class': 'cbi-value-title' }, _('Console port')),
				E('div', { 'class': 'cbi-value-field' }, [ port, E('div', { 'class': 'cbi-value-description' }, _('Default 7890. Must not be used by LuCI, SSH or a bridge.')) ])
			]),
			E('div', { 'class': 'cbi-value' }, [
				E('label', { 'class': 'cbi-value-title' }, _('Allow LAN access')),
				E('div', { 'class': 'cbi-value-field' }, [ lan, E('div', { 'class': 'cbi-value-description' }, _('LAN devices can open the console after logging in: through "Open console" here (no password needed), or with the LAN password set in the console.')) ])
			]),
			E('div', { 'class': 'cbi-value' }, [
				E('label', { 'class': 'cbi-value-title' }, ''),
				E('div', { 'class': 'cbi-value-field' }, E('button', {
					'class': 'cbi-button cbi-button-save',
					'click': ui.createHandlerFn(this, function() {
						var p = parseInt(port.value, 10);
						if (!(p >= 1 && p <= 65535)) {
							notifyError(_('The port must be between 1 and 65535.'));
							return;
						}
						return callConsole(p, lan.checked).then(checked).then(function(r) {
							ui.addNotification(null, E('p', {}, r.live === false
								? _('Saved. rsagent is not running; the change applies when it starts.')
								: _('Saved. The console now uses port %d.').format(r.port)), 'info');
							if (r.warning)
								ui.addNotification(null, E('p', {}, txt(r.warning)), 'warning');
							return self.refresh(true);
						}).catch(notifyError);
					})
				}, _('Save')))
			])
		]);
	},

	// ---------------------------------------------------------------- page
	refresh: function(withSettings) {
		var self = this;
		return callStatus().then(function(st) {
			self.update(st, withSettings);
		}).catch(function(e) { self.update({ error: String(e) }, false); });
	},

	update: function(st, withSettings) {
		dom.content(this.tsNode, this.renderTailscale(st));
		dom.content(this.statusNode, this.renderStatus(st));
		dom.content(this.bridgesNode, this.renderBridges(st));
		dom.content(this.portsNode, this.renderPorts(st));
		// The settings form is only rebuilt on demand so polling does not overwrite what is being typed.
		if (withSettings)
			dom.content(this.settingsNode, this.renderSettings(st));
	},

	render: function(st) {
		this.tsNode = E('div');
		this.statusNode = E('div');
		this.bridgesNode = E('div');
		this.settingsNode = E('div');
		this.portsNode = E('div');
		this.keyInput = E('input', { 'type': 'password', 'class': 'cbi-input-password', 'autocomplete': 'off', 'placeholder': 'tskey-…', 'style': 'width:100%;max-width:32em' });
		this.tagsInput = E('input', { 'class': 'cbi-input-text', 'placeholder': 'tag:serial-agent', 'style': 'width:100%;max-width:32em' });
		this.update(st, true);

		poll.add(L.bind(function() { return this.refresh(false); }, this), 5);

		return E('div', {}, [
			E('h2', {}, _('RemoteSerial')),
			E('div', { 'class': 'cbi-map-descr' }, _('Serial-to-TCP bridges over Tailscale. This page covers the basics; use the console for editing bridges, the terminal, captures and logs.')),
			this.tsNode,
			this.statusNode,
			this.bridgesNode,
			this.portsNode,
			this.settingsNode
		]);
	},

	handleSaveApply: null,
	handleSave: null,
	handleReset: null
});
