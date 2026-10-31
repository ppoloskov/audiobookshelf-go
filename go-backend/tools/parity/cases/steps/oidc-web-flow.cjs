'use strict'

module.exports = async function runOIDCWebFlow({ step, runtime }) {
  const { scenarios, args, fixture, provider, work, runFlow } = runtime
  const node = await runFlow('node', scenarios[step.scenario].node, args['go-bin'], provider, work, fixture, step.mode || 'success')
  const go = await runFlow('go', scenarios[step.scenario].go, args['go-bin'], provider, work, fixture, step.mode || 'success')
  return { node, go }
}
