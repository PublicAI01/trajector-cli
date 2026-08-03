package lifecycle

import (
	"errors"
	"fmt"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
)

// pairingTimeout bounds how long Login waits for browser approval.
const pairingTimeout = 15 * time.Minute

// Login pairs this device, or completes the signed-in state when it is
// already paired. Either way it ends in the same place: token stored,
// recording resumed, discovery hint installed.
func (m *Machine) Login(io IO) error {
	if m.Paired() {
		fmt.Fprintln(io.Out, "This device is already paired.")
		return m.finishLogin(io)
	}
	return m.Pair(io)
}

// Pair runs the browser pairing flow and signs the device in.
func (m *Machine) Pair(io IO) error {
	pairing, err := m.deps.Platform.StartPairing(m.deps.Version)
	if err != nil {
		return fmt.Errorf("starting pairing: %w", err)
	}
	fmt.Fprintf(io.Out, "To pair this device, open:\n\n  %s\n\n", pairing.VerificationURL)
	if pairing.UserCode != "" {
		fmt.Fprintf(io.Out, "and confirm that the page shows code %s.\n", pairing.UserCode)
	}
	fmt.Fprintln(io.Out, "Waiting for approval...")

	deadline := m.deps.Now().Add(pairingTimeout)
	for {
		result, err := m.deps.Platform.PollPairing(pairing.PairingID)
		if err != nil {
			return fmt.Errorf("checking pairing: %w", err)
		}
		switch result.Status {
		case platform.PairingPaired:
			if err := m.deps.Tokens.Save(tokenstore.DeviceTokenName, []byte(result.DeviceToken)); err != nil {
				return fmt.Errorf("storing the device token: %w", err)
			}
			fmt.Fprintln(io.Out, "Device paired.")
			return m.finishLogin(io)
		case platform.PairingExpired:
			return errors.New("the pairing link expired; run `trajector login` again")
		}
		if m.deps.Now().After(deadline) {
			return errors.New("timed out waiting for approval; run `trajector login` again")
		}
		time.Sleep(pairing.PollInterval())
	}
}

// finishLogin applies the signed-in side effects that must hold whether
// this was a fresh pairing or a re-login.
func (m *Machine) finishLogin(io IO) error {
	if err := m.routes.Resume(pauseSignedOut); err != nil {
		return err
	}
	userSettings := claudesettings.UserSettingsPath(m.deps.Home)
	if !claudesettings.HasHook(userSettings, claudesettings.DiscoveryMarker) {
		if err := claudesettings.InjectUserHook(userSettings, hookCommand(m.deps.ExecPath, "discovery")); err != nil {
			return fmt.Errorf("installing the discovery hint: %w", err)
		}
	}
	fmt.Fprintln(io.Out, "Recording is active for enabled projects. Run `trajector enable` inside a project to add it.")
	return nil
}

// Logout signs the device out: the token is revoked with the service
// and removed locally, and recording pauses everywhere. Forwarding for
// enabled projects is untouched, and their grants survive.
func (m *Machine) Logout(io IO) error {
	token, paired := m.deviceToken()
	if !paired {
		fmt.Fprintln(io.Out, "This device is not paired; nothing to do.")
		return nil
	}
	if err := m.deps.Platform.RevokeDevice(token); err != nil {
		fmt.Fprintf(io.Err, "trajector: warning: could not revoke the token with the service (%v); it was removed locally, revoke it from your account page as well\n", err)
	}
	if err := m.deps.Tokens.Delete(tokenstore.DeviceTokenName); err != nil && !errors.Is(err, tokenstore.ErrNotFound) {
		return fmt.Errorf("removing the device token: %w", err)
	}
	if err := m.routes.Pause(pauseSignedOut); err != nil {
		return fmt.Errorf("pausing recording: %w", err)
	}
	fmt.Fprintln(io.Out, "Signed out. Forwarding for enabled projects is unaffected; recording is")
	fmt.Fprintln(io.Out, "paused everywhere until you run `trajector login` again, and kept data")
	fmt.Fprintln(io.Out, "uploads once you are back.")
	return nil
}
