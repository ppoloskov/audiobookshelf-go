'use strict'

module.exports = async function runOIDCMobileInvalidVerifier({ step, runtime }) {
  const { scenarios, args, provider, work, runMobileInvalidVerifier } = runtime
  const node = await runMobileInvalidVerifier('node', scenarios[step.scenario].node, args['go-bin'], provider, work)
  const go = await runMobileInvalidVerifier('go', scenarios[step.scenario].go, args['go-bin'], provider, work)
  return { node, go }
}
