package catalog

import (
	"encoding/json"
	"slices"
)

var resettableCorrectionFields = []string{"name", "biography", "department_id", "category_id", "nature_id", "code", "credits"}

func validCorrectionReset(in Correction) bool {
	if len(in.ResetFields) > len(resettableCorrectionFields) {
		return false
	}
	seen := map[string]bool{}
	for _, field := range in.ResetFields {
		if !slices.Contains(resettableCorrectionFields, field) || seen[field] {
			return false
		}
		seen[field] = true
	}
	if seen["name"] && in.Name != nil ||
		seen["biography"] && in.Biography != nil ||
		seen["department_id"] && in.DepartmentID != nil ||
		seen["category_id"] && in.CategoryID != nil ||
		seen["nature_id"] && in.NatureID != nil ||
		seen["code"] && in.Code != nil ||
		seen["credits"] && (in.Credits != nil || in.ClearCredits) {
		return false
	}
	return true
}

func applyManual(record *Record) error {
	for field, value := range record.Manual {
		switch field {
		case "inactive_reason":
		case "inactive":
			record.Object.Inactive = value == "true"
		case "name":
			record.Object.Name = value
		case "biography":
			record.Object.Biography = value
		case "department_id":
			record.Object.DepartmentID = value
		case "category_id":
			record.Object.CategoryID = value
		case "nature_id":
			record.Object.NatureID = value
		case "code":
			record.Object.Code = value
		case "credits":
			if json.Unmarshal([]byte(value), &record.Object.Credits) != nil {
				return ErrUnavailable
			}
		default:
			return ErrUnavailable
		}
	}
	return nil
}

func resetImportedField(record *Record, field string) {
	delete(record.Manual, field)
	switch field {
	case "name":
		record.Object.Name = record.Imported.Name
	case "biography":
		record.Object.Biography = record.Imported.Biography
	case "department_id":
		record.Object.DepartmentID = record.Imported.DepartmentID
	case "category_id":
		record.Object.CategoryID = record.Imported.CategoryID
	case "nature_id":
		record.Object.NatureID = record.Imported.NatureID
	case "code":
		record.Object.Code = record.Imported.Code
	case "credits":
		if record.Imported.Credits == nil {
			record.Object.Credits = nil
		} else {
			v := *record.Imported.Credits
			record.Object.Credits = &v
		}
	}
}

func recordCorrection(record *Record, in Correction) error {
	if record.Manual == nil {
		record.Manual = map[string]string{}
	}
	for _, field := range in.ResetFields {
		resetImportedField(record, field)
	}
	for field, value := range map[string]*string{"name": in.Name, "biography": in.Biography, "department_id": in.DepartmentID, "category_id": in.CategoryID, "nature_id": in.NatureID, "code": in.Code} {
		if value != nil {
			record.Manual[field] = *value
		}
	}
	if in.ClearCredits {
		record.Manual["credits"] = "null"
	}
	if in.Credits != nil {
		value, err := json.Marshal(in.Credits)
		if err != nil {
			return ErrInput
		}
		record.Manual["credits"] = string(value)
	}
	return applyManual(record)
}
