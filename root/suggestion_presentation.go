package root

// The terminal presenter is the shared publication boundary for captured
// suggestion results. It does not write to the shell or run application code.
func presentSuggestionGeneration(presenter *terminalUIPresenter, pipeline *suggestionPipeline, generation uint64, update func(func(func() []byte))) {
	presenter.Update(func(compose func(func() []byte)) {
		// Keep the generation decision and publication indivisible relative to
		// preference changes. A check before presenter entry leaves a stale
		// payload able to overwrite a newer frame after waiting for this lock.
		pipeline.mu.Lock()
		defer pipeline.mu.Unlock()
		if pipeline.generation == generation {
			update(compose)
		}
	})
}
