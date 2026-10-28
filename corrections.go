package catalog

import "encoding/json"

func applyManual(record *Record) error {
	for field, value := range record.Manual {
		switch field {
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
func recordCorrection(record *Record, in Correction) error {
	if record.Manual == nil {
		record.Manual = map[string]string{}
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
