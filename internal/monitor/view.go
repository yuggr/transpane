package monitor

import (
	"github.com/yuggr/transpane/internal/domain"
	"github.com/yuggr/transpane/internal/protection"
)

// View is an immutable snapshot of one displayed message plus its mutable
// translation state. The monitor deep-copies maps before publishing.
type View struct {
	Message      domain.Message
	Doc          *protection.ProtectedDoc
	State        domain.TranslationState
	Translations map[int]string
	Pending      map[int]struct{}
	Failed       map[int]struct{}
	ErrorLabels  map[string]struct{}
}

func newView(message domain.Message) View {
	doc := protection.ProtectDocument(message.Original)
	state := initialState(doc)
	if message.Kind == domain.MessageKindUser {
		state = domain.TranslationStateSkipped
	}
	return View{
		Message:      message,
		Doc:          doc,
		State:        state,
		Translations: map[int]string{},
		Pending:      map[int]struct{}{},
		Failed:       map[int]struct{}{},
		ErrorLabels:  map[string]struct{}{},
	}
}

func initialState(doc *protection.ProtectedDoc) domain.TranslationState {
	if len(doc.TranslatableUnits()) > 0 {
		return domain.TranslationStateOriginal
	}
	if doc.HasConservativeExclusion() {
		return domain.TranslationStatePartial
	}
	return domain.TranslationStateSkipped
}

func (v View) DisplayText() string {
	if len(v.Translations) > 0 {
		return v.Doc.Render(v.Translations)
	}
	return v.Message.Original
}

func (v *View) RefreshState() {
	if len(v.Pending) > 0 {
		if len(v.Translations) > 0 {
			v.State = domain.TranslationStateTranslating
		} else {
			v.State = domain.TranslationStateQueued
		}
		return
	}
	units := v.Doc.TranslatableUnits()
	if len(units) == 0 {
		v.State = initialState(v.Doc)
		return
	}
	succeeded := 0
	for _, unit := range units {
		if _, ok := v.Translations[unit.ID]; ok {
			succeeded++
		}
	}
	switch {
	case succeeded == len(units):
		if v.Doc.HasConservativeExclusion() {
			v.State = domain.TranslationStatePartial
		} else {
			v.State = domain.TranslationStateTranslated
		}
	case succeeded > 0:
		v.State = domain.TranslationStatePartial
	case len(v.Failed) > 0:
		v.State = domain.TranslationStateFailed
	case v.Doc.HasConservativeExclusion():
		v.State = domain.TranslationStatePartial
	default:
		v.State = domain.TranslationStateOriginal
	}
}

func cloneView(view View) View {
	clone := view
	clone.Translations = make(map[int]string, len(view.Translations))
	for key, value := range view.Translations {
		clone.Translations[key] = value
	}
	clone.Pending = make(map[int]struct{}, len(view.Pending))
	for key := range view.Pending {
		clone.Pending[key] = struct{}{}
	}
	clone.Failed = make(map[int]struct{}, len(view.Failed))
	for key := range view.Failed {
		clone.Failed[key] = struct{}{}
	}
	clone.ErrorLabels = make(map[string]struct{}, len(view.ErrorLabels))
	for key := range view.ErrorLabels {
		clone.ErrorLabels[key] = struct{}{}
	}
	return clone
}

func cloneViews(views []View) []View {
	clones := make([]View, len(views))
	for index, view := range views {
		clones[index] = cloneView(view)
	}
	return clones
}
