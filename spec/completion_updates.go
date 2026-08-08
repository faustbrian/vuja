package spec

var completionUpdates = make(chan struct{}, 1)

func CompletionUpdates() <-chan struct{} {
	return completionUpdates
}

func notifyCompletionUpdate() {
	select {
	case completionUpdates <- struct{}{}:
	default:
	}
}

// NotifyCompletionUpdate asks interactive consumers to refresh after an
// asynchronous built-in source has changed.
func NotifyCompletionUpdate() {
	notifyCompletionUpdate()
}
