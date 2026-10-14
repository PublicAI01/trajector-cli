package claudesettings

import "testing"

func TestRequestedSettings_StateWhatLeavingARequiredOneOffCosts(t *testing.T) {
	for _, s := range RequestedSettings {
		if s.Required != (s.OffCost != "") {
			t.Errorf("%s: Required = %v but OffCost = %q; a required setting states its cost and no other does",
				s.Key, s.Required, s.OffCost)
		}
	}
}
