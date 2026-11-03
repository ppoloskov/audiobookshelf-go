package contracttest

// journeySteps is the replacement trial's client journey (journeySteps in
// tools/parity/replacement-trial.cjs), in order. The steps mutate the fixture,
// so the parts run in this order and must not be reordered.
func journeySteps(ids map[string]string) []journeyStep {
	var steps []journeyStep
	steps = append(steps, journeyStepsPlayback(ids)...)
	steps = append(steps, journeyStepsPodcastAndAccounts(ids)...)
	steps = append(steps, journeyStepsFeeds(ids)...)
	steps = append(steps, journeyStepsAuthorsSeries(ids)...)
	steps = append(steps, journeyStepsCatalog(ids)...)
	return steps
}
