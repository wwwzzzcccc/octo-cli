package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/spf13/cobra"

	"github.com/Mininglamp-OSS/octo-cli/internal/cmdutil"
)

const maxFreedrawPoints = 100000

type freedrawInput struct {
	points      []any
	pressures   []any
	lastPoint   any
	hasPoints   bool
	hasPressure bool
	hasLast     bool
}

func loadFreedrawInputs(c *cobra.Command, f *cmdutil.Factory, o *sceneFlags) (*freedrawInput, error) {
	in := &freedrawInput{hasPoints: c.Flags().Changed("points"), hasPressure: c.Flags().Changed("pressures"), hasLast: c.Flags().Changed("last-committed-point")}
	if !in.hasPoints && !in.hasPressure && !in.hasLast && !c.Flags().Changed("simulate-pressure") {
		return nil, errors.New("set at least one of --points, --pressures, --simulate-pressure, or --last-committed-point")
	}
	var err error
	if in.hasPoints {
		in.points, err = loadJSONArrayInput(f, "--points", o.points, false)
		if err != nil {
			return nil, err
		}
		if err := validateFreedrawPoints(in.points); err != nil {
			return nil, fmt.Errorf("--points: %w", err)
		}
	}
	if in.hasPressure {
		in.pressures, err = loadJSONArrayInput(f, "--pressures", o.pressures, true)
		if err != nil {
			return nil, err
		}
		if err := validatePressures(in.pressures); err != nil {
			return nil, fmt.Errorf("--pressures: %w", err)
		}
	}
	if in.hasPoints && in.hasPressure && len(in.pressures) != 0 && len(in.pressures) != len(in.points) {
		return nil, errors.New("--pressures must be empty or have exactly the same length as --points")
	}
	if in.hasLast {
		in.lastPoint, err = loadNullablePoint(f, o.lastCommittedPoint)
		if err != nil {
			return nil, fmt.Errorf("--last-committed-point: %w", err)
		}
	}
	return in, nil
}

func loadNullablePoint(f *cmdutil.Factory, spec string) (any, error) {
	raw, err := cmdutil.ParseInput(f, spec)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, errors.New("must be JSON [x,y] or null")
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("must contain exactly one JSON value")
	}
	if value == nil {
		return nil, nil
	}
	if err := validateFreedrawPoints([]any{value}); err != nil {
		return nil, errors.New("must be a finite [x,y] point or null")
	}
	return value, nil
}

func validateFreedrawPoints(points []any) error {
	if len(points) == 0 {
		return errors.New("must contain at least one point")
	}
	if len(points) > maxFreedrawPoints {
		return fmt.Errorf("must contain at most %d points", maxFreedrawPoints)
	}
	for i, raw := range points {
		p, ok := raw.([]any)
		if !ok || len(p) != 2 {
			return fmt.Errorf("point %d must be [x,y]", i)
		}
		for _, rawCoordinate := range p {
			n, ok := finiteNumber(rawCoordinate)
			if !ok || math.Abs(n) > 1e7 {
				return fmt.Errorf("point %d coordinates must be finite and within ±10000000", i)
			}
		}
	}
	return nil
}

func validatePressures(pressures []any) error {
	if len(pressures) > maxFreedrawPoints {
		return fmt.Errorf("must contain at most %d samples", maxFreedrawPoints)
	}
	for i, raw := range pressures {
		n, ok := finiteNumber(raw)
		if !ok || n < 0 || n > 1 {
			return fmt.Errorf("sample %d must be a finite number between 0 and 1", i)
		}
	}
	return nil
}

func applyFreedrawMutation(c *cobra.Command, e map[string]any, in *freedrawInput) error {
	if e["type"] != "freedraw" {
		return errors.New("freedraw geometry requires a freedraw target")
	}
	points, _ := e["points"].([]any)
	pressures, _ := e["pressures"].([]any)
	if in.hasPoints {
		points, e["points"] = in.points, in.points
	}
	if in.hasPressure {
		pressures, e["pressures"] = in.pressures, in.pressures
	}
	if len(pressures) != 0 && len(pressures) != len(points) {
		return errors.New("freedraw pressures must be empty or have exactly the same length as points")
	}
	if c.Flags().Changed("simulate-pressure") {
		e["simulatePressure"] = c.Flag("simulate-pressure").Value.String() == "true"
	}
	if in.hasLast {
		e["lastCommittedPoint"] = in.lastPoint
	}
	if in.hasPoints {
		if err := normalizeLocalPoints(e); err != nil {
			return err
		}
	}
	updateLinearBounds(e)
	return nil
}

func validateFreedrawElement(e map[string]any) error {
	points, ok := e["points"].([]any)
	if !ok {
		return errors.New("freedraw points must be an array")
	}
	if err := validateFreedrawPoints(points); err != nil {
		return fmt.Errorf("freedraw points: %w", err)
	}
	pressures, ok := e["pressures"].([]any)
	if !ok {
		return errors.New("freedraw pressures must be an array")
	}
	if err := validatePressures(pressures); err != nil {
		return fmt.Errorf("freedraw pressures: %w", err)
	}
	if len(pressures) != 0 && len(pressures) != len(points) {
		return errors.New("freedraw pressures must be empty or have exactly the same length as points")
	}
	if _, ok := e["simulatePressure"].(bool); !ok {
		return errors.New("freedraw simulatePressure must be boolean")
	}
	if last := e["lastCommittedPoint"]; last != nil {
		if err := validateFreedrawPoints([]any{last}); err != nil {
			return errors.New("freedraw lastCommittedPoint must be a finite [x,y] point or null")
		}
	}
	return nil
}
