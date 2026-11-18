'use strict'

module.exports = async function runNotificationLifecycle({ runtime }) {
  const { selectedRun, requestedCases, bookLibraryId, referenceAccessToken, results, runStoredCase, compare, startNotificationAppriseFixture, ioClient, node, go, PREFIX, socketNotificationProbe, call, delay } = runtime
    if (!selectedRun || [...requestedCases].some(id => id.startsWith('email-'))) {
    await runStoredCase("email-settings-read-defaults-initial", { "referenceAccessToken": referenceAccessToken })
    await runStoredCase("email-settings-patch-coercion", { "referenceAccessToken": referenceAccessToken })
    await runStoredCase("email-settings-read-after-coercion", { "referenceAccessToken": referenceAccessToken })
    }
    if (!selectedRun || [...requestedCases].some(id => id.startsWith('notification-'))) {
    await runStoredCase("notifications-settings-read-defaults-initial", { "referenceAccessToken": referenceAccessToken })
    await runStoredCase("notifications-settings-patch-custom", { "referenceAccessToken": referenceAccessToken })
    await runStoredCase("notifications-settings-read-after-custom", { "referenceAccessToken": referenceAccessToken })
    await runStoredCase("notifications-settings-patch-defaults", { "referenceAccessToken": referenceAccessToken })
    await runStoredCase("notifications-settings-read-after-defaults", { "referenceAccessToken": referenceAccessToken })
    await runStoredCase("notifications-clear-apprise-url", { "referenceAccessToken": referenceAccessToken })
    }
    const notificationCreateBody = {
      libraryId: bookLibraryId,
      eventName: 'onTest',
      urls: ['discord://parity-test'],
      titleTemplate: 'Parity notification',
      bodyTemplate: 'Test body',
      enabled: false,
      type: 'info'
    }
    let nodeNotification = { body: { notifications: [] } }
    let goNotification = { body: { notifications: [] } }
    let nodeNotificationID = null
    let goNotificationID = null
    ;[nodeNotification, goNotification] = await runStoredCase("notification-create", { "notificationCreateBody": notificationCreateBody, "referenceAccessToken": referenceAccessToken })
    nodeNotificationID = nodeNotification.body?.notifications?.at(-1)?.id
    goNotificationID = goNotification.body?.notifications?.at(-1)?.id
    if (!selectedRun || [...requestedCases].some(id => id.startsWith('notification-'))) if (nodeNotificationID && goNotificationID) {
      await runStoredCase("notification-test-existing-id-unconfigured", { "goNotificationID": goNotificationID, "nodeNotificationID": nodeNotificationID, "referenceAccessToken": referenceAccessToken })
      const apprise = await startNotificationAppriseFixture()
      try {
        await runStoredCase("notification-apprise-fixture-configure-success", { "apprise.url": apprise.url, "referenceAccessToken": referenceAccessToken })
        await runStoredCase("notification-test-send-success", { "goNotificationID": goNotificationID, "nodeNotificationID": nodeNotificationID, "referenceAccessToken": referenceAccessToken })
        if (apprise.requests.node.length !== 1 || apprise.requests.go.length !== 1) throw new Error('notification test success must deliver exactly once on each side')
        results.push(compare('notification-test-apprise-payload',
          { status: 200, headers: {}, body: apprise.requests.node[0].payload },
          { status: 200, headers: {}, body: apprise.requests.go[0].payload }))
        const [nodeAfterSuccess, goAfterSuccess] = await runStoredCase("notification-read-after-test-success", { "referenceAccessToken": referenceAccessToken })
        const testState = (response, notificationID) => {
          const notification = response.body?.settings?.notifications?.find(entry => entry.id === notificationID)
          if (!notification) throw new Error('notification disappeared after test delivery')
          return {
            lastFiredAt: notification.lastFiredAt,
            lastAttemptFailed: notification.lastAttemptFailed,
            numConsecutiveFailedAttempts: notification.numConsecutiveFailedAttempts,
            numTimesFired: notification.numTimesFired
          }
        }
        results.push(compare('notification-test-no-state-change-success',
          { status: 200, headers: {}, body: testState(nodeAfterSuccess, nodeNotificationID) },
          { status: 200, headers: {}, body: testState(goAfterSuccess, goNotificationID) }))
        if (JSON.stringify(testState(nodeAfterSuccess, nodeNotificationID)) !== JSON.stringify({ lastFiredAt: null, lastAttemptFailed: false, numConsecutiveFailedAttempts: 0, numTimesFired: 0 }) ||
            JSON.stringify(testState(goAfterSuccess, goNotificationID)) !== JSON.stringify({ lastFiredAt: null, lastAttemptFailed: false, numConsecutiveFailedAttempts: 0, numTimesFired: 0 })) {
          throw new Error('GET /notifications/:id/test must not mutate firing counters')
        }
        await runStoredCase("notification-apprise-fixture-configure-failure", { "apprise.url": apprise.url, "referenceAccessToken": referenceAccessToken })
        await runStoredCase("notification-test-send-failure", { "goNotificationID": goNotificationID, "nodeNotificationID": nodeNotificationID, "referenceAccessToken": referenceAccessToken })
        if (apprise.requests.node.length !== 2 || apprise.requests.go.length !== 2) throw new Error('notification test failure must attempt exactly one delivery on each side')
        const [nodeAfterFailure, goAfterFailure] = await runStoredCase("notification-read-after-test-failure", { "referenceAccessToken": referenceAccessToken })
        results.push(compare('notification-test-no-state-change-failure',
          { status: 200, headers: {}, body: testState(nodeAfterFailure, nodeNotificationID) },
          { status: 200, headers: {}, body: testState(goAfterFailure, goNotificationID) }))
        if (JSON.stringify(testState(nodeAfterFailure, nodeNotificationID)) !== JSON.stringify({ lastFiredAt: null, lastAttemptFailed: false, numConsecutiveFailedAttempts: 0, numTimesFired: 0 }) ||
            JSON.stringify(testState(goAfterFailure, goNotificationID)) !== JSON.stringify({ lastFiredAt: null, lastAttemptFailed: false, numConsecutiveFailedAttempts: 0, numTimesFired: 0 })) {
          throw new Error('failed GET /notifications/:id/test must not mutate firing counters')
        }
      } finally {
        await new Promise(resolve => apprise.server.close(resolve))
      }
    } else {
      for (const id of ['notification-test-existing-id-unconfigured', 'notification-apprise-fixture-configure-success', 'notification-test-send-success', 'notification-test-apprise-payload', 'notification-read-after-test-success', 'notification-test-no-state-change-success', 'notification-apprise-fixture-configure-failure', 'notification-test-send-failure', 'notification-read-after-test-failure', 'notification-test-no-state-change-failure']) {
        results.push({ id, passed: false, skipped: true, prerequisite: 'both notification create responses must return UUIDs' })
      }
    }
    await runStoredCase("notification-create-empty-urls", { "referenceAccessToken": referenceAccessToken })
    await runStoredCase("notification-create-minimal", { "referenceAccessToken": referenceAccessToken })
    await runStoredCase("notification-read-after-create", { "referenceAccessToken": referenceAccessToken })
    if (nodeNotificationID && goNotificationID) {
      await runStoredCase("notification-update-without-body-id", { "goNotificationID": goNotificationID, "nodeNotificationID": nodeNotificationID, "referenceAccessToken": referenceAccessToken })
      await runStoredCase("notification-update", { "goNotificationID": goNotificationID, "nodeNotificationID": nodeNotificationID, "referenceAccessToken": referenceAccessToken })
      const [nodeReadAfterUpdate, goReadAfterUpdate] = await runStoredCase("notification-read-after-update", { "referenceAccessToken": referenceAccessToken })
      if (!selectedRun || [...requestedCases].some(id => id.startsWith('notification-event-'))) {
      const activeOnTest = response => response.body.settings.notifications.filter(entry => entry.eventName === 'onTest' && entry.enabled)
      const nodeActiveOnTest = activeOnTest(nodeReadAfterUpdate)
      const goActiveOnTest = activeOnTest(goReadAfterUpdate)
      if (nodeActiveOnTest.length !== 2 || goActiveOnTest.length !== 2) throw new Error('global test event fixture must contain two enabled onTest notifications')
      const globalApprise = await startNotificationAppriseFixture()
      try {
        await runStoredCase("notification-apprise-global-configure-success", { "globalApprise.url": globalApprise.url, "referenceAccessToken": referenceAccessToken })
        if (ioClient) {
          const eventProbes = await Promise.all([
            socketNotificationProbe(ioClient, node, `${PREFIX}/socket.io`, referenceAccessToken, '/api/notifications/test', call),
            socketNotificationProbe(ioClient, go, `${PREFIX}/socket.io`, referenceAccessToken, '/api/notifications/test', call)
          ])
          results.push(compare('notification-event-test-send-success', eventProbes[0].response, eventProbes[1].response))
          const broadcast = compare('notification-event-broadcast-success',
            { status: 200, headers: {}, body: { count: eventProbes[0].events.length, events: eventProbes[0].events } },
            { status: 200, headers: {}, body: { count: eventProbes[1].events.length, events: eventProbes[1].events } })
          if (eventProbes[0].events.length !== 1 || eventProbes[1].events.length !== 1) {
            broadcast.passed = false
            broadcast.differences = [...(broadcast.differences || []), { path: '$.count', reference: eventProbes[0].events.length, go: eventProbes[1].events.length, expected: 1 }]
          }
          results.push(broadcast)
        } else {
          await runStoredCase("notification-event-test-send-success", { "referenceAccessToken": referenceAccessToken })
          results.push({ id: 'notification-event-broadcast-success', passed: false, skipped: true, prerequisite: 'socket.io-client v4 is required to observe notifications_updated' })
        }
        if (globalApprise.requests.node.length !== 2 || globalApprise.requests.go.length !== 2) throw new Error('global notification test must deliver once per enabled notification on each side')
        results.push(compare('notification-event-test-delivery-payloads',
          { status: 200, headers: {}, body: globalApprise.requests.node.map(request => request.payload) },
          { status: 200, headers: {}, body: globalApprise.requests.go.map(request => request.payload) }))
        const stateShape = response => response.body.settings.notifications.filter(entry => entry.eventName === 'onTest').map(entry => ({
          urls: entry.urls,
          enabled: entry.enabled,
          lastFiredAtPresent: entry.lastFiredAt !== null,
          lastAttemptFailed: entry.lastAttemptFailed,
          numConsecutiveFailedAttempts: entry.numConsecutiveFailedAttempts,
          numTimesFired: entry.numTimesFired
        }))
        const [nodeAfterGlobalSuccess, goAfterGlobalSuccess] = await runStoredCase("notification-read-after-event-test-success", { "referenceAccessToken": referenceAccessToken })
        const nodeSuccessState = stateShape(nodeAfterGlobalSuccess)
        const goSuccessState = stateShape(goAfterGlobalSuccess)
        for (const notification of [...nodeSuccessState, ...goSuccessState]) {
          if (!notification.enabled || !notification.lastFiredAtPresent || notification.lastAttemptFailed || notification.numConsecutiveFailedAttempts !== 0 || notification.numTimesFired !== 1) {
            throw new Error(`global notification success state is unexpected: ${JSON.stringify(notification)}`)
          }
        }
        results.push(compare('notification-event-test-state-success',
          { status: 200, headers: {}, body: nodeSuccessState },
          { status: 200, headers: {}, body: goSuccessState }))
        await delay(1100)
        await runStoredCase("notification-event-test-repeat-success", { "referenceAccessToken": referenceAccessToken })
        if (globalApprise.requests.node.length !== 4 || globalApprise.requests.go.length !== 4) throw new Error('repeated notification test must redeliver each enabled notification on both sides')
        results.push(compare('notification-event-test-repeat-payloads',
          { status: 200, headers: {}, body: globalApprise.requests.node.slice(2).map(request => request.payload) },
          { status: 200, headers: {}, body: globalApprise.requests.go.slice(2).map(request => request.payload) }))
        const [nodeAfterGlobalRepeat, goAfterGlobalRepeat] = await runStoredCase("notification-read-after-event-test-repeat", { "referenceAccessToken": referenceAccessToken })
        const nodeRepeatState = stateShape(nodeAfterGlobalRepeat)
        const goRepeatState = stateShape(goAfterGlobalRepeat)
        for (const notification of [...nodeRepeatState, ...goRepeatState]) {
          if (!notification.enabled || !notification.lastFiredAtPresent || notification.lastAttemptFailed || notification.numConsecutiveFailedAttempts !== 0 || notification.numTimesFired !== 2) {
            throw new Error(`repeated global notification state is unexpected: ${JSON.stringify(notification)}`)
          }
        }
        results.push(compare('notification-event-test-state-repeat',
          { status: 200, headers: {}, body: nodeRepeatState },
          { status: 200, headers: {}, body: goRepeatState }))
        await delay(1100)
        await runStoredCase("notification-event-test-set-failure-threshold", { "globalApprise.url": globalApprise.url, "referenceAccessToken": referenceAccessToken })
        if (ioClient) {
          const eventProbes = await Promise.all([
            socketNotificationProbe(ioClient, node, `${PREFIX}/socket.io`, referenceAccessToken, '/api/notifications/test?fail=1', call),
            socketNotificationProbe(ioClient, go, `${PREFIX}/socket.io`, referenceAccessToken, '/api/notifications/test?fail=1', call)
          ])
          results.push(compare('notification-event-test-intentional-failure', eventProbes[0].response, eventProbes[1].response))
          const broadcast = compare('notification-event-broadcast-failure',
            { status: 200, headers: {}, body: { count: eventProbes[0].events.length, events: eventProbes[0].events } },
            { status: 200, headers: {}, body: { count: eventProbes[1].events.length, events: eventProbes[1].events } })
          if (eventProbes[0].events.length !== 1 || eventProbes[1].events.length !== 1) {
            broadcast.passed = false
            broadcast.differences = [...(broadcast.differences || []), { path: '$.count', reference: eventProbes[0].events.length, go: eventProbes[1].events.length, expected: 1 }]
          }
          results.push(broadcast)
        } else {
          await runStoredCase("notification-event-test-intentional-failure", { "referenceAccessToken": referenceAccessToken })
          results.push({ id: 'notification-event-broadcast-failure', passed: false, skipped: true, prerequisite: 'socket.io-client v4 is required to observe notifications_updated' })
        }
        if (globalApprise.requests.node.length !== 4 || globalApprise.requests.go.length !== 4) throw new Error('intentional test-event failure must not deliver to Apprise')
        results.push(compare('notification-event-test-no-delivery-on-failure',
          { status: 200, headers: {}, body: globalApprise.requests.node.length },
          { status: 200, headers: {}, body: globalApprise.requests.go.length }))
        const [nodeAfterGlobalFailure, goAfterGlobalFailure] = await runStoredCase("notification-read-after-event-test-failure", { "referenceAccessToken": referenceAccessToken })
        const nodeFailureState = stateShape(nodeAfterGlobalFailure)
        const goFailureState = stateShape(goAfterGlobalFailure)
        for (const notification of [...nodeFailureState, ...goFailureState]) {
          if (notification.enabled || !notification.lastFiredAtPresent || !notification.lastAttemptFailed || notification.numConsecutiveFailedAttempts !== 1 || notification.numTimesFired !== 3) {
            throw new Error(`global notification failure state is unexpected: ${JSON.stringify(notification)}`)
          }
        }
        results.push(compare('notification-event-test-state-failure',
          { status: 200, headers: {}, body: nodeFailureState },
          { status: 200, headers: {}, body: goFailureState }))
      } finally {
        await new Promise(resolve => globalApprise.server.close(resolve))
      }
      }
      if (!selectedRun || [...requestedCases].some(id => id.startsWith('notification-'))) await runStoredCase("notification-delete", { "goNotificationID": goNotificationID, "nodeNotificationID": nodeNotificationID, "referenceAccessToken": referenceAccessToken })
      if (!selectedRun || [...requestedCases].some(id => id.startsWith('notification-'))) await runStoredCase("notification-read-after-delete", { "referenceAccessToken": referenceAccessToken })
    } else {
      for (const id of ['notification-update', 'notification-read-after-update', 'notification-apprise-global-configure-success', 'notification-event-test-send-success', 'notification-event-test-delivery-payloads', 'notification-read-after-event-test-success', 'notification-event-test-state-success', 'notification-event-test-repeat-success', 'notification-event-test-repeat-payloads', 'notification-read-after-event-test-repeat', 'notification-event-test-state-repeat', 'notification-event-test-intentional-failure', 'notification-event-test-no-delivery-on-failure', 'notification-read-after-event-test-failure', 'notification-event-test-state-failure', 'notification-delete', 'notification-read-after-delete']) {
        results.push({ id, passed: false, skipped: true, prerequisite: 'both notification create responses must return UUIDs' })
      }
    }
    if (!selectedRun || [...requestedCases].some(id => id.startsWith('notification-'))) await runStoredCase("notification-update-missing", { "referenceAccessToken": referenceAccessToken })
    if (!selectedRun || [...requestedCases].some(id => id.startsWith('notification-'))) await runStoredCase("notification-delete-missing", { "referenceAccessToken": referenceAccessToken })
  
}
