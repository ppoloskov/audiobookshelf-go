'use strict'

const { executeCaseGroup } = require('../../lib/case-data.cjs')

module.exports = function runBookmarkLifecycle({ caseFile, group, context, selectedRun, requestedCases, comparePair }) {
  return executeCaseGroup({ cases: caseFile, group, context, selectedRun, requestedCases, comparePair })
}
