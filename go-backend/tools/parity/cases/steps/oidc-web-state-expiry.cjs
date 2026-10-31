'use strict'

module.exports = async function runOIDCWebStateExpiry({ step, runtime }) {
  const { scenarios, args, provider, work, runWebStateExpiryPair } = runtime
  return {
    webStateExpiry: await runWebStateExpiryPair(
      scenarios[step.scenario].node,
      scenarios[step.scenario].go,
      args['go-bin'],
      provider,
      work
    )
  }
}
