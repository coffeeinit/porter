import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import typescript from 'typescript'

function loadModule(path, dependencies = {}) {
	const source = readFileSync(new URL(path, import.meta.url), 'utf8')
	const compiled = typescript.transpileModule(source, {
		compilerOptions: { module: typescript.ModuleKind.CommonJS },
	}).outputText
	const exports = {}
	runInNewContext(compiled, { exports, require: (name) => dependencies[name] })
	return exports
}

const { getTeamSwitcherRows } = loadModule('../src/lib/teamSwitcher.ts')
const { capabilityLabel, groupCapabilitiesByCategory } = loadModule(
	'../src/lib/capabilities.ts',
	{
		'@/lib/format': {
			capitalise: (text) => text.charAt(0).toUpperCase() + text.slice(1),
		},
	},
)

test('duplicate labels use distinct owners, including case and surrounding spaces', () => {
	const rows = getTeamSwitcherRows([
		{ name: 'team-a', label: 'Northwind', owner: 'design@example.com' },
		{ name: 'team-b', label: ' northwind ', owner: 'platform@example.com' },
	])
	assert.equal(rows[0].detail, 'design@example.com')
	assert.equal(rows[1].detail, 'platform@example.com')
	assert.notEqual(rows[0].identity, rows[1].identity)
})

test('duplicate labels with the same or missing owner use unique team IDs', () => {
	for (const owner of ['support@example.com', null]) {
		const rows = getTeamSwitcherRows([
			{ name: 'team-staging', label: 'Customer Services', owner },
			{ name: 'team-production', label: 'Customer Services', owner },
		])
		assert.equal(rows[0].detail, 'team-staging')
		assert.equal(rows[1].detail, 'team-production')
	}
})

test('a unique long name keeps its complete identity without an extra detail line', () => {
	const label = "Prathamesh Kurunkar's Team"
	const [row] = getTeamSwitcherRows([{ name: 'team-a', label, owner: null }])
	assert.equal(row.identity, label)
	assert.equal(row.detail, '')
	assert.equal(getTeamSwitcherRows([]).length, 0)
})

test('identifier-style permission labels are readable without changing the capability name', () => {
	const capability = { name: 'ACT_ON_CUSTOMER', description: 'ACT_ON_CUSTOMER' }
	assert.equal(capabilityLabel(capability), 'Act on customer')
	const [group] = groupCapabilitiesByCategory([capability])
	assert.equal(group.label, 'Act on customer')
	assert.equal(group.caps[0].name, 'ACT_ON_CUSTOMER')
})
