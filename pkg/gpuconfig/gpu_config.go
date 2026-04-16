package gpuconfig

import (
	"fmt"
	"time"
)

type GPUSlotConfigProfile struct {
	Categories []GPUCategory
}

type GPUCategory struct {
	// ResourceType determines whether this category provisions a CPU or GPU notebook.
	// Allowed values: "cpu" | "gpu"
	ResourceType string

	Name                    string
	DisplayName             string
	Description             string
	InstanceType            string
	GPUMemory               string
	MaxActiveBookings       int
	MaxBookingsPerWeek      int
	AdvanceBookingDays      int
	MinAdvanceBookingDays   int
	NoShowGraceMins         int
	MaxConcurrentUsers      int
	MaxContiguousSlotSelectionAllowed int

	// Access control
	// When true, the booking requires profile credit gating (existing can_create_gpu_notebook).
	RequiresCredits bool
	// Optional role required for this category (e.g. "compute" for GPU categories).
	RequiredRole string

	GPUType                 string
	GPURequest              int
	GPULimit                int
	CPURequest              string
	CPULimit                string
	MemoryRequest           string
	MemoryLimit             string
	StorageSize             string
	PreShutdownWarningMins  int
	ShutdownGracePeriodMins int
	SortOrder               int
	Slots                   []SlotTemplate
}

type SlotTemplate struct {
	Key           string
	Label         string
	StartTime     string
	EndTime       string
	DurationHours float64
	SpansMidnight bool
}

// GPUSlotConfigs is code-defined GPU slot/category configuration.
// The active profile is selected by env.
var GPUSlotConfigs = map[string]GPUSlotConfigProfile{
	"production": {
		Categories: []GPUCategory{
			{
				ResourceType:        "cpu",
				Name:                    "cpu_basic",
				DisplayName:             "CPU Basic",
				Description:             "Standard CPU notebook",
				InstanceType:            "",
				GPUMemory:               "",
				// CPU booking rules
				MaxActiveBookings:       2,
				MaxBookingsPerWeek:      7,
				AdvanceBookingDays:      30,
				MinAdvanceBookingDays:   0,
				NoShowGraceMins:         15,
				MaxConcurrentUsers:      2,
				MaxContiguousSlotSelectionAllowed: 2,
				RequiresCredits:         false,
				RequiredRole:            "",
				// CPU resources
				// Conservative dev sizing: improves scheduling on saturated t3a.large nodes.
				CPURequest:              "0.25",
				CPULimit:                "1",
				MemoryRequest:           "512Mi",
				MemoryLimit:             "1Gi",
				StorageSize:             "10Gi",
				PreShutdownWarningMins: 10,
				ShutdownGracePeriodMins: 5,
				SortOrder:               1,
				Slots: []SlotTemplate{
					{Key: "cpu_basic_08:00", Label: "4h Session", StartTime: "08:00", EndTime: "12:00", DurationHours: 4},
					{Key: "cpu_basic_12:00", Label: "4h Session", StartTime: "12:00", EndTime: "16:00", DurationHours: 4},
					{Key: "cpu_basic_16:00", Label: "4h Session", StartTime: "16:00", EndTime: "20:00", DurationHours: 4},
					{Key: "cpu_basic_20:00", Label: "4h Session", StartTime: "20:00", EndTime: "00:00", DurationHours: 4, SpansMidnight: true},
					{Key: "cpu_basic_00:00", Label: "4h Session", StartTime: "00:00", EndTime: "04:00", DurationHours: 4},
					{Key: "cpu_basic_04:00", Label: "4h Session", StartTime: "04:00", EndTime: "08:00", DurationHours: 4},
				},
			},
			{
				ResourceType:        "gpu",
				Name:                    "advance",
				DisplayName:             "GPU Advance",
				Description:             "40 GB NVIDIA A100 GPU",
				InstanceType:            "p4d.24xlarge",
				GPUMemory:               "40 GB",
				MaxActiveBookings:       1,
				MaxBookingsPerWeek:      3,
				AdvanceBookingDays:      30,
				MinAdvanceBookingDays:   3,
				NoShowGraceMins:         15,
				MaxConcurrentUsers:      8,
				MaxContiguousSlotSelectionAllowed: 2,
				RequiresCredits:         true,
				RequiredRole:            "compute",
				GPUType:                 "nvidia.com/gpu",
				GPURequest:              1,
				GPULimit:                1,
				CPURequest:              "2",
				CPULimit:                "3",
				MemoryRequest:           "8Gi",
				MemoryLimit:             "10Gi",
				StorageSize:             "50Gi",
				PreShutdownWarningMins:  15,
				ShutdownGracePeriodMins: 5,
				SortOrder:               2,
				Slots: []SlotTemplate{
					{Key: "advance_10:00", Label: "22h Session", StartTime: "10:00", EndTime: "08:00", DurationHours: 22, SpansMidnight: true},
				},
			},
			{
				ResourceType:        "gpu",
				Name:                    "basic",
				DisplayName:             "GPU Basic",
				Description:             "16 GB NVIDIA T4 GPU",
				InstanceType:            "g4dn.xlarge",
				GPUMemory:               "16 GB",
				MaxActiveBookings:       1,
				MaxBookingsPerWeek:      3,
				AdvanceBookingDays:      30,
				MinAdvanceBookingDays:   0,
				NoShowGraceMins:         15,
				MaxConcurrentUsers:      28,
				MaxContiguousSlotSelectionAllowed: 2,
				RequiresCredits:         true,
				RequiredRole:            "compute",
				GPUType:                 "nvidia.com/gpu",
				GPURequest:              1,
				GPULimit:                1,
				CPURequest:              "2",
				CPULimit:                "3",
				MemoryRequest:           "8Gi",
				MemoryLimit:             "10Gi",
				StorageSize:             "50Gi",
				PreShutdownWarningMins:  15,
				ShutdownGracePeriodMins: 5,
				SortOrder:               10,
				Slots: []SlotTemplate{
					{Key: "basic_08:00", Label: "4h Session", StartTime: "08:00", EndTime: "12:00", DurationHours: 4},
					{Key: "basic_12:00", Label: "4h Session", StartTime: "12:00", EndTime: "16:00", DurationHours: 4},
					{Key: "basic_16:00", Label: "4h Session", StartTime: "16:00", EndTime: "20:00", DurationHours: 4},
					{Key: "basic_20:00", Label: "4h Session", StartTime: "20:00", EndTime: "00:00", DurationHours: 4, SpansMidnight: true},
					{Key: "basic_00:00", Label: "4h Session", StartTime: "00:00", EndTime: "04:00", DurationHours: 4},
					{Key: "basic_04:00", Label: "4h Session", StartTime: "04:00", EndTime: "08:00", DurationHours: 4},
				},
			},
		},
	},
}

func ValidateGPUSlotConfigProfile(profile string) error {
	cfg, ok := GPUSlotConfigs[profile]
	if !ok {
		return fmt.Errorf("unknown GPU slot config profile: %s", profile)
	}

	seenCategory := map[string]struct{}{}
	for _, category := range cfg.Categories {
		if category.Name == "" {
			return fmt.Errorf("gpu category name cannot be empty")
		}
		if _, exists := seenCategory[category.Name]; exists {
			return fmt.Errorf("duplicate gpu category name: %s", category.Name)
		}
		seenCategory[category.Name] = struct{}{}

		switch category.ResourceType {
		case "cpu", "gpu":
			// ok
		default:
			return fmt.Errorf("category %s has invalid ResourceType: %q", category.Name, category.ResourceType)
		}

		// InstanceType + GPU fields are required only for GPU categories.
		if category.ResourceType == "gpu" {
			if category.InstanceType == "" {
				return fmt.Errorf("gpu category %s has empty instance type", category.Name)
			}
			if category.GPUType == "" {
				return fmt.Errorf("gpu category %s has empty GPUType", category.Name)
			}
			if category.GPURequest <= 0 || category.GPULimit <= 0 {
				return fmt.Errorf("gpu category %s must have GPURequest/GPULimit > 0", category.Name)
			}
		}

		if len(category.Slots) == 0 {
			return fmt.Errorf("gpu category %s has no slots", category.Name)
		}
		if category.MaxContiguousSlotSelectionAllowed <= 0 {
			return fmt.Errorf("gpu category %s must have MaxContiguousSlotSelectionAllowed > 0", category.Name)
		}

		seenSlot := map[string]struct{}{}
		for _, slot := range category.Slots {
			if slot.Key == "" {
				return fmt.Errorf("gpu category %s has slot with empty key", category.Name)
			}
			if _, exists := seenSlot[slot.Key]; exists {
				return fmt.Errorf("gpu category %s has duplicate slot key: %s", category.Name, slot.Key)
			}
			seenSlot[slot.Key] = struct{}{}

			if _, err := time.Parse("15:04", slot.StartTime); err != nil {
				return fmt.Errorf("gpu category %s slot %s has invalid start time: %w", category.Name, slot.Key, err)
			}
			if _, err := time.Parse("15:04", slot.EndTime); err != nil {
				return fmt.Errorf("gpu category %s slot %s has invalid end time: %w", category.Name, slot.Key, err)
			}
			if slot.DurationHours <= 0 {
				return fmt.Errorf("gpu category %s slot %s has non-positive duration", category.Name, slot.Key)
			}
		}
	}
	return nil
}

func GetGPUCategory(profile, categoryName string) (*GPUCategory, bool) {
	cfg, ok := GPUSlotConfigs[profile]
	if !ok {
		return nil, false
	}
	for _, c := range cfg.Categories {
		if c.Name == categoryName {
			category := c
			return &category, true
		}
	}
	return nil, false
}

func GetGPUSlotTemplate(profile, categoryName, slotKey string) (*SlotTemplate, bool) {
	category, ok := GetGPUCategory(profile, categoryName)
	if !ok {
		return nil, false
	}
	for _, s := range category.Slots {
		if s.Key == slotKey {
			slot := s
			return &slot, true
		}
	}
	return nil, false
}
