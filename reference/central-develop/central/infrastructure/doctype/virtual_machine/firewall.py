from __future__ import annotations

import json

from central.server_models import FirewallChange, FirewallRule

ANYWHERE = ["0.0.0.0/0", "::/0"]

# Rules Central adds to every inbound list and hides from the customer.
# 1. The region's mesh, which the console and the regional gateway use.
# 2. WireGuard gateway clients. Atlas still drops them unless the VM opts in to gateway access.
# 3. ICMP, which IPv6 neighbour discovery and path MTU need.
SYSTEM_INBOUND_RULES = [
	{"protocol": "any", "ports": "", "cidrs": ["fdaa::/16"]},
	{"protocol": "any", "ports": "", "cidrs": ["fdac::/16"]},
	{"protocol": "icmp", "ports": "", "cidrs": ANYWHERE},
]

# Every new server gets these rules, so turning its firewall on later keeps SSH, HTTP
# and HTTPS open instead of blocking everything.
WEB_SERVER_FIREWALL = FirewallChange(
	enabled=True,
	inbound=[FirewallRule(protocol="tcp", ports=port, cidrs=ANYWHERE) for port in ("22", "80", "443")],
	outbound=[FirewallRule(protocol="any", cidrs=ANYWHERE)],
)


def get_new_server_firewall(is_enabled: bool) -> dict:
	"""The Atlas firewall for a new server: the web server rules, on or off."""
	return get_atlas_firewall(WEB_SERVER_FIREWALL.model_copy(update={"enabled": is_enabled}))


def get_atlas_firewall(change: FirewallChange) -> dict:
	"""The firewall fields Atlas replaces, with Central's rules before the customer's inbound
	rules. Atlas keeps the rules of a disabled firewall."""
	remote = {}
	if change.enabled is not None:
		remote["enabled"] = change.enabled
	if change.inbound is not None:
		remote["inbound"] = SYSTEM_INBOUND_RULES + [rule.model_dump() for rule in change.inbound]
	if change.outbound is not None:
		remote["outbound"] = [rule.model_dump() for rule in change.outbound]

	return remote


def get_customer_rules(remote: dict) -> dict:
	"""The customer's rules from a firewall Atlas reports, without Central's own."""

	def normalize(rule: dict) -> dict:
		return {"protocol": rule["protocol"], "ports": rule.get("ports") or "", "cidrs": rule["cidrs"]}

	inbound = [normalize(rule) for rule in remote.get("inbound") or []]
	return {
		"inbound": [rule for rule in inbound if rule not in SYSTEM_INBOUND_RULES],
		"outbound": [normalize(rule) for rule in remote.get("outbound") or []],
	}


def get_observed_firewall(network: dict) -> dict:
	"""The Virtual Machine fields for the firewall Atlas reports in a VM's network."""
	remote = network.get("firewall") or {}

	return {
		"is_firewall_enabled": bool(remote.get("enabled")),
		"firewall_rules": json.dumps(get_customer_rules(remote), sort_keys=True),
	}
