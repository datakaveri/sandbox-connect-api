package gpuconfig

import "testing"

func TestValidateGPUSlotConfigProfileBrowserCategory(t *testing.T) {
	const profile = "test_browser_category"
	GPUSlotConfigs[profile] = GPUSlotConfigProfile{
		Categories: []GPUCategory{
			{
				ResourceType: "browser",
				Name:         "jupyter_lite",
				DisplayName:  "Jupyter Lite (Free)",
				Description:  "Browser-only JupyterLite environment",
				IsBookable:   false,
				LaunchMode:   "direct",
				LaunchURL:    "/jupyterlite/lab/index.html",
				PriceLabel:   "Free",
				Persistence:  "browser_local",
			},
		},
	}
	defer delete(GPUSlotConfigs, profile)

	if err := ValidateGPUSlotConfigProfile(profile); err != nil {
		t.Fatalf("expected browser category config to validate, got %v", err)
	}
}

func TestValidateGPUSlotConfigProfileBrowserCategoryRequiresLaunchURL(t *testing.T) {
	const profile = "test_browser_category_missing_launch"
	GPUSlotConfigs[profile] = GPUSlotConfigProfile{
		Categories: []GPUCategory{
			{
				ResourceType: "browser",
				Name:         "jupyter_lite",
				IsBookable:   false,
				LaunchMode:   "direct",
			},
		},
	}
	defer delete(GPUSlotConfigs, profile)

	if err := ValidateGPUSlotConfigProfile(profile); err == nil {
		t.Fatal("expected missing browser launch URL to fail validation")
	}
}

func TestValidateGPUSlotConfigProfileBookableCategoryRequiresSlots(t *testing.T) {
	const profile = "test_cpu_category_missing_slots"
	GPUSlotConfigs[profile] = GPUSlotConfigProfile{
		Categories: []GPUCategory{
			{
				ResourceType:                      "cpu",
				Name:                              "cpu_basic",
				IsBookable:                        true,
				MaxContiguousSlotSelectionAllowed: 1,
			},
		},
	}
	defer delete(GPUSlotConfigs, profile)

	if err := ValidateGPUSlotConfigProfile(profile); err == nil {
		t.Fatal("expected bookable category without slots to fail validation")
	}
}

func TestValidateGPUSlotConfigProfileRejectsInvalidResourceType(t *testing.T) {
	const profile = "test_invalid_resource_type"
	GPUSlotConfigs[profile] = GPUSlotConfigProfile{
		Categories: []GPUCategory{
			{
				ResourceType: "serverless",
				Name:         "bad_category",
				IsBookable:   false,
			},
		},
	}
	defer delete(GPUSlotConfigs, profile)

	if err := ValidateGPUSlotConfigProfile(profile); err == nil {
		t.Fatal("expected invalid resource type to fail validation")
	}
}

func TestProductionCPUBasicBookingCapacity(t *testing.T) {
	category, ok := GetGPUCategory("production", "cpu_basic")
	if !ok {
		t.Fatal("expected production cpu_basic category")
	}
	if category.MaxActiveBookings != 2 {
		t.Fatalf("MaxActiveBookings = %d, want 2", category.MaxActiveBookings)
	}
	if category.MaxConcurrentUsers != 4 {
		t.Fatalf("MaxConcurrentUsers = %d, want 4", category.MaxConcurrentUsers)
	}
}
