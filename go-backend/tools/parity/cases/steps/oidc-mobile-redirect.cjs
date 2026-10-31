'use strict'

module.exports = async function runOIDCMobileRedirect({ step, runtime }) {
  const { scenarios, args, provider, work, runMobileRedirect } = runtime
  const node = await runMobileRedirect('node', scenarios[step.scenario].node, args['go-bin'], provider, work)
  const go = await runMobileRedirect('go', scenarios[step.scenario].go, args['go-bin'], provider, work)
  return { node, go }
}
