package catalog

import (
	"context"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

type TeacherHomepagePublicProfile struct {
	*TeacherHomepage
	Name string `json:"name,omitempty"`
}

type TeacherHomepagePublication struct {
	ContactsAvailable bool                          `json:"contacts_available,omitempty"`
	Profile           *TeacherHomepagePublicProfile `json:"profile,omitempty"`
}

type homepagePresentation struct {
	directory homepageDirectory
	controls  TeacherHomepageControls
	allowed   map[TeacherHomepageFieldSelector]bool
	rules     map[TeacherHomepageFieldSelector]TeacherHomepageFieldRule
	hidden    map[string]bool
}

func readTeacherHomepagePresentation(t Transaction, teacher Object) (homepagePresentation, error) {
	port, ok := t.(TeacherHomepageControlsTransaction)
	if !ok {
		return homepagePresentation{}, ErrUnavailable
	}
	controls, err := port.TeacherHomepageControls(teacher.SchoolID, teacherHomepageID(teacher))
	if err != nil {
		return homepagePresentation{}, err
	}
	list, err := loadTeacherHomepageAllowlist(t, teacher.SchoolID)
	if err != nil {
		return homepagePresentation{}, err
	}
	return newHomepagePresentation(controls, list), nil
}

func newHomepagePresentation(controls TeacherHomepageControls, list TeacherHomepageAllowlist) homepagePresentation {
	p := homepagePresentation{
		controls: controls,
		allowed:  make(map[TeacherHomepageFieldSelector]bool, len(list.Fields)),
		rules:    make(map[TeacherHomepageFieldSelector]TeacherHomepageFieldRule, len(controls.Rules)),
		hidden:   make(map[string]bool),
	}
	for _, field := range list.Fields {
		p.allowed[teacherHomepageSelectorKey(field)] = true
	}
	for _, rule := range controls.Rules {
		key := teacherHomepageSelectorKey(rule.Field)
		p.rules[key] = rule
		if !rule.Hidden {
			continue
		}
		if key.Label == "" {
			p.hidden[key.Field] = true
		} else if alias := teacherHomepageAliases[key.Label]; alias != "" {
			p.hidden[alias] = true
		}
	}
	return p
}

func (p homepagePresentation) permitsPage() bool {
	return p.controls.PageVisibility == TeacherHomepageVisible
}

func (p homepagePresentation) photo() (bool, *TeacherHomepageFieldPhoto) {
	if !p.permitsPage() || p.hidden["photo"] {
		return false, nil
	}
	key := TeacherHomepageFieldSelector{Field: "photo"}
	rule := p.rules[key]
	if rule.Override != nil && rule.Override.Photo != nil {
		return true, rule.Override.Photo
	}
	return p.allowed[key], nil
}

var teacherHomepageAliases = map[string]string{
	"姓名": "name", "name": "name",
	"主要任职": "position", "职务": "position", "position": "position", "administrative position": "position",
	"学历": "education", "education": "education", "学位": "degree", "degree": "degree", "学科": "discipline", "discipline": "discipline",
	"邮箱": "email", "电子邮箱": "email", "电子邮件": "email", "电子信箱": "email", "e-mail": "email", "email": "email",
	"email address": "email", "e-mail address": "email",
	"电话": "phone", "联系电话": "phone", "办公电话": "phone", "手机": "phone", "联系方式": "phone",
	"phone": "phone", "telephone": "phone", "tel": "phone", "contact": "phone", "contact information": "phone", "phone number": "phone",
	"办公室": "office_location", "办公地点": "office_location", "办公地址": "office_location",
	"office": "office_location", "office address": "office_location", "office location": "office_location", "office room": "office_location",
	"通讯地址": "office_location", "联系地址": "office_location", "mailing address": "office_location", "contact address": "office_location",
	"入职时间": "hire_date", "入校时间": "hire_date", "到校时间": "hire_date", "任教时间": "hire_date",
	"date of employment": "hire_date", "date joined": "hire_date", "hire date": "hire_date",
	"个人简介": "biography", "简介": "biography", "profile": "biography", "biography": "biography", "personal profile": "biography",
	"研究方向": "research", "研究领域": "research", "research interests": "research", "research directions": "research",
	"research field": "research", "research fields": "research",
	"导师类别": "supervisors", "博士生导师": "supervisors", "硕士生导师": "supervisors", "supervisors": "supervisors",
}

func homepageScalar(profile *TeacherHomepage, field string) *string {
	switch field {
	case "name":
		return &profile.Name
	case "position":
		return &profile.Position
	case "education":
		return &profile.Education
	case "degree":
		return &profile.Degree
	case "discipline":
		return &profile.Discipline
	case "biography":
		return &profile.Biography
	case "research":
		return &profile.Research
	case "email":
		return &profile.Email
	case "phone":
		return &profile.Phone
	case "office_location":
		return &profile.OfficeLocation
	default:
		return &profile.HireDate
	}
}

var homepageEmail = regexp.MustCompile(`(?i)[a-z0-9.!#$%&'*+/=?^_\x60{|}~-]+@[a-z0-9-]+(?:\.[a-z0-9-]+)+`)
var homepagePhone = regexp.MustCompile(`\+?[0-9][0-9 ().-]{5,}[0-9]`)
var homepageLabelPrefix = regexp.MustCompile(`^[ \t]*([^\n:：]{1,160})[：:]`)
var homepageNumericValue = regexp.MustCompile(`^[0-9][0-9 ./:\-年月日]*$`)
var homepageLineBreaks = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\u0085", "\n", "\u2028", "\n", "\u2029", "\n")

type homepageCopy struct {
	value, field string
	hard         bool
	ordinary     bool
}

type homepageRedaction struct {
	directory        homepageDirectory
	excluded, hidden map[string]bool
	copies           map[homepageCopy]bool
	contactLines     map[string]bool
	contactMatcher   *strings.Replacer
	keepOrdinary     bool
}

func (p homepagePresentation) redaction(source TeacherHomepage) homepageRedaction {
	r := homepageRedaction{directory: p.directory, excluded: map[string]bool{}, hidden: p.hidden, copies: map[homepageCopy]bool{}, contactLines: map[string]bool{}}
	for _, field := range teacherHomepageScalarFields {
		key := TeacherHomepageFieldSelector{Field: field}
		r.excluded[field] = p.hidden[field] || !p.allowed[key]
		if r.excluded[field] || p.rules[key].Override != nil {
			r.add(field, *homepageScalar(&source, field), p.hidden[field])
		}
	}
	key := TeacherHomepageFieldSelector{Field: "supervisors"}
	r.excluded[key.Field] = p.hidden[key.Field] || !p.allowed[key]
	if r.excluded[key.Field] || p.rules[key].Override != nil {
		for _, value := range source.Supervisors {
			r.add(key.Field, value, p.hidden[key.Field])
		}
	}
	for _, field := range source.Fields {
		r.suppressValue(p, &source, "fields", field.Label, field.Value)
	}
	for _, section := range source.Sections {
		r.suppressValue(p, &source, "sections", section.Title, section.Text)
	}
	for _, rule := range p.controls.Rules {
		if rule.Override != nil {
			r.suppressHiddenOverride(p, rule)
		}
	}
	var contactPatterns []string
	for key := range r.contactLines {
		if utf8.RuneCountInString(key) >= 4 {
			contactPatterns = append(contactPatterns, key, "")
		}
	}
	if len(contactPatterns) > 0 {
		r.contactMatcher = strings.NewReplacer(contactPatterns...)
	}
	return r
}

func (r homepageRedaction) suppressValue(p homepagePresentation, source *TeacherHomepage, group, label, value string) {
	value = stripHomepageContactMarker(value)
	key := teacherHomepageSelectorKey(TeacherHomepageFieldSelector{Field: group, Label: label})
	alias := teacherHomepageAliases[key.Label]
	hard := p.hidden[group] || p.rules[key].Hidden || p.hidden[alias]
	unlisted := !p.allowed[key] && !p.isAllowedScalarCopy(source, alias, value)
	if hard || p.rules[key].Override != nil ||
		alias != "" && (r.excluded[alias] || p.rules[TeacherHomepageFieldSelector{Field: alias}].Override != nil) {
		r.add(alias, value, hard)
	} else if contactLabel(key.Label) && p.hidden["phone"] {
		for _, phone := range homepagePhone.FindAllString(value, -1) {
			r.add("phone", phone, true)
		}
	} else if unlisted {
		if text := teacherHomepageLabelKey(value); text != "" {
			r.copies[homepageCopy{value: text, field: alias, ordinary: true}] = true
		}
	}
}

func (p homepagePresentation) isAllowedScalarCopy(source *TeacherHomepage, field, value string) bool {
	if field == "" || !p.allowed[TeacherHomepageFieldSelector{Field: field}] {
		return false
	}
	actual := teacherHomepageLabelKey(value)
	if field == "supervisors" {
		return slices.Contains(source.Supervisors, actual)
	}
	return teacherHomepageLabelKey(*homepageScalar(source, field)) == actual
}

func (r homepageRedaction) suppressHiddenOverride(p homepagePresentation, rule TeacherHomepageFieldRule) {
	key := teacherHomepageSelectorKey(rule.Field)
	alias := teacherHomepageAliases[key.Label]
	v := rule.Override
	hidden := rule.Hidden || p.hidden[key.Field] || p.hidden[alias]
	if v.Text != nil && hidden {
		if alias == "" && key.Label == "" {
			alias = key.Field
		}
		r.add(alias, *v.Text, true)
	}
	if hidden && v.Supervisors != nil {
		for _, value := range *v.Supervisors {
			r.add("supervisors", value, true)
		}
	}
	if v.Fields != nil {
		for _, item := range *v.Fields {
			r.suppressOverrideItem(p, key.Field, item.Label, item.Value)
		}
	}
	if v.Sections != nil {
		for _, item := range *v.Sections {
			r.suppressOverrideItem(p, key.Field, item.Title, item.Text)
		}
	}
}

func (r homepageRedaction) suppressOverrideItem(p homepagePresentation, group, label, value string) {
	key := teacherHomepageSelectorKey(TeacherHomepageFieldSelector{Field: group, Label: label})
	alias := teacherHomepageAliases[key.Label]
	if p.hidden[group] || p.rules[key].Hidden || p.hidden[alias] {
		r.add(alias, value, true)
	}
}

func (r homepageRedaction) add(field, value string, hard bool) {
	value = homepageLineBreaks.Replace(stripHomepageContactMarker(value))
	key := teacherHomepageLabelKey(value)
	if key == "" {
		return
	}
	hiddenContact := hard && (field == "office_location" || field == "phone" || field == "email")
	if hard {
		for _, email := range homepageEmail.FindAllString(key, -1) {
			r.copies[homepageCopy{value: email, field: "email", hard: true}] = true
		}
		for _, phone := range homepagePhone.FindAllString(value, -1) {
			if digits := homepageDigits(phone); len(digits) >= 7 {
				r.copies[homepageCopy{value: digits, field: "phone", hard: true}] = true
			}
		}
	}
	if field == "phone" || hiddenContact && strings.Contains(value, "\n") {
		for line := range strings.SplitSeq(value, "\n") {
			r.addContactLine(field, line, hard)
		}
	}
	r.copies[homepageCopy{value: key, field: field, hard: hard}] = true
}

func (r homepageRedaction) addContactLine(field, line string, hard bool) {
	key := teacherHomepageLabelKey(line)
	if field == "phone" {
		for _, item := range homepagePhone.FindAllString(key, -1) {
			if digits := homepageDigits(item); len(digits) >= 7 {
				r.copies[homepageCopy{value: digits, field: field, hard: hard}] = true
			}
		}
	}
	if !hard || key == "" || homepageNumericValue.MatchString(key) || teacherHomepageAliases[key] != "" {
		return
	}
	if label := homepageLabelPrefix.FindStringSubmatchIndex(key); label != nil && teacherHomepageAliases[teacherHomepageLabelKey(key[label[2]:label[3]])] == field {
		key = teacherHomepageLabelKey(key[label[1]:])
	}
	// An isolated room number or year can also occur in scholarly prose.
	if compact := homepageContactKey(key); strings.ContainsFunc(compact, unicode.IsLetter) {
		r.contactLines[compact] = true
	}
}

func homepageContactKey(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return r
		}
		return -1
	}, value)
}

func (r homepageRedaction) clean(value string, explicit bool) string {
	if value == "" {
		return ""
	}
	value = homepageLineBreaks.Replace(stripHomepageContactMarker(value))
	lines := make([]string, 0, 1+strings.Count(value, "\n"))
	for line := range strings.SplitSeq(value, "\n") {
		original := line
		line = r.directory.renameText(line)
		normalized := teacherHomepageLabelKey(line)
		if label := homepageLabelPrefix.FindStringSubmatch(line); label != nil {
			key := teacherHomepageLabelKey(label[1])
			alias := teacherHomepageAliases[key]
			if r.hidden[alias] || !explicit && r.excluded[alias] {
				continue
			}
			value, keep := r.directory.value(key, line[len(label[0]):])
			if !keep || teacherHomepageRecordLabels[key] {
				continue
			}
			line = label[0] + value
		}
		if teacherHomepageRecordLabels[normalized] ||
			(r.hidden["email"] || !explicit && r.excluded["email"]) && homepageEmail.MatchString(normalized) ||
			r.contains(normalized, explicit) || original != line && r.contains(teacherHomepageLabelKey(original), explicit) {
			continue
		}
		lines = append(lines, line)
	}
	text := strings.TrimSpace(strings.Join(lines, "\n"))
	if r.contains(teacherHomepageLabelKey(text), explicit) {
		return ""
	}
	return text
}

func (r homepageRedaction) contains(text string, explicit bool) bool {
	if len(r.contactLines) > 0 {
		compact := homepageContactKey(text)
		if r.contactLines[compact] || r.contactMatcher != nil && r.contactMatcher.Replace(compact) != compact {
			return true
		}
	}
	for copy := range r.copies {
		if explicit && !copy.hard || r.keepOrdinary && copy.ordinary {
			continue
		}
		if text == copy.value ||
			utf8.RuneCountInString(copy.value) >= 2 && !homepageNumericValue.MatchString(copy.value) && strings.Contains(text, copy.value) {
			return true
		}
		if copy.field == "phone" && len(copy.value) >= 7 {
			for _, candidate := range homepagePhone.FindAllString(text, -1) {
				if homepageDigits(candidate) == copy.value {
					return true
				}
			}
		}
	}
	return false
}

func homepageDigits(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, value)
}

type homepageTextItem struct {
	label, text string
	overridden  bool
}

func (p homepagePresentation) dynamicItems(group string, values []homepageTextItem, redaction *homepageRedaction) []homepageTextItem {
	if p.hidden[group] {
		return nil
	}
	groupOverride := p.rules[TeacherHomepageFieldSelector{Field: group}].Override != nil
	out := make([]homepageTextItem, 0, len(values))
	seen := map[TeacherHomepageFieldSelector]bool{}
	for _, item := range values {
		key := teacherHomepageSelectorKey(TeacherHomepageFieldSelector{Field: group, Label: item.label})
		rule := p.rules[key]
		if rule.Override != nil {
			// A long source section may have several chunks with the same
			// heading. One replacement supersedes all of its old chunks.
			if seen[key] {
				continue
			}
			item.text = *rule.Override.Text
			item.overridden = true
		}
		seen[key] = true
		if item, ok := p.dynamicItem(key, item, rule, groupOverride, redaction); ok {
			out = append(out, item)
		}
	}
	for _, rule := range p.controls.Rules {
		key := teacherHomepageSelectorKey(rule.Field)
		if key.Field != group || key.Label == "" || seen[key] || rule.Hidden || rule.Override == nil {
			continue
		}
		item := homepageTextItem{label: rule.Field.Label, text: *rule.Override.Text, overridden: true}
		if item, ok := p.dynamicItem(key, item, rule, false, redaction); ok {
			out = append(out, item)
		}
	}
	return out
}

func boundHomepageItems(out []homepageTextItem, limit int) []homepageTextItem {
	for len(out) > limit {
		i := slices.IndexFunc(out, func(item homepageTextItem) bool { return !item.overridden })
		if i < 0 {
			return out[:limit]
		}
		out = slices.Delete(out, i, i+1)
	}
	return out
}

func (p homepagePresentation) fieldItems(fields []TeacherHomepageField, redaction *homepageRedaction) []homepageTextItem {
	if rule := p.rules[TeacherHomepageFieldSelector{Field: "fields"}]; rule.Override != nil {
		fields = *rule.Override.Fields
	}
	items := make([]homepageTextItem, 0, len(fields))
	for _, field := range fields {
		items = append(items, homepageTextItem{label: field.Label, text: field.Value})
	}
	return p.dynamicItems("fields", items, redaction)
}

func (p homepagePresentation) sectionItems(sections []TeacherHomepageSection, redaction *homepageRedaction) []homepageTextItem {
	if rule := p.rules[TeacherHomepageFieldSelector{Field: "sections"}]; rule.Override != nil {
		sections = *rule.Override.Sections
	}
	items := make([]homepageTextItem, 0, len(sections))
	for _, section := range sections {
		items = append(items, homepageTextItem{label: section.Title, text: section.Text})
	}
	return p.dynamicItems("sections", items, redaction)
}

func validateTeacherHomepageOverrideCapacity(controls TeacherHomepageControls) error {
	p := newHomepagePresentation(controls, TeacherHomepageAllowlist{})
	// Reserve manual items independently of source labels and contact copies.
	// The display rules still exclude explicitly hidden items and groups.
	if len(p.fieldItems(nil, nil)) > teacherHomepageFieldLimit ||
		len(p.sectionItems(nil, nil)) > teacherHomepageSectionLimit {
		return ErrTeacherHomepageCapacity
	}
	return nil
}

func (p homepagePresentation) dynamicItem(key TeacherHomepageFieldSelector, item homepageTextItem, rule TeacherHomepageFieldRule, groupOverride bool, redaction *homepageRedaction) (homepageTextItem, bool) {
	alias := teacherHomepageAliases[key.Label]
	if key.Label == "" || rule.Hidden || p.hidden[alias] {
		return item, false
	}
	explicit := item.overridden || groupOverride
	if !explicit && alias != "" && (!p.allowed[TeacherHomepageFieldSelector{Field: alias}] || p.rules[TeacherHomepageFieldSelector{Field: alias}].Override != nil) {
		return item, false
	}
	if !explicit && !p.allowed[key] {
		// Outside a listed heading only explicitly written email addresses are eligible.
		// Keep the rest of the unlisted field or section private.
		email := TeacherHomepageFieldSelector{Field: "email"}
		if redaction == nil || p.hidden["email"] || !p.allowed[email] || p.rules[email].Override != nil || teacherHomepageRecordLabels[key.Label] {
			return item, false
		}
		policy := *redaction
		policy.keepOrdinary = true
		var addresses []string
		for _, address := range homepageEmail.FindAllString(stripHomepageContactMarker(item.text), -1) {
			if policy.clean(address, false) != "" && !slices.Contains(addresses, address) {
				addresses = append(addresses, address)
			}
		}
		if len(addresses) == 0 {
			return item, false
		}
		item.text = strings.Join(addresses, "\n")
		item.label = "邮箱"
		redaction = &policy
	}
	if redaction != nil {
		item.text = redaction.clean(item.text, explicit)
		value, keep := redaction.directory.value(key.Label, item.text)
		if !keep {
			return item, false
		}
		item.text = value
		item.label = redaction.clean(item.label, explicit)
	}
	item.overridden = explicit
	return item, item.text != "" && item.label != ""
}

func (p homepagePresentation) profile(source *TeacherHomepage, teacher string) *TeacherHomepage {
	return p.renderProfile(source, teacher, nil)
}

func (p homepagePresentation) renderProfile(source *TeacherHomepage, teacher string, placements homepageMediaPlacements) *TeacherHomepage {
	if !p.permitsPage() || source == nil && !p.controls.Independent {
		return nil
	}
	if source == nil {
		source = &TeacherHomepage{}
	}
	out := &TeacherHomepage{}
	redaction := p.redaction(*source)
	for _, field := range teacherHomepageScalarFields {
		if p.hidden[field] {
			continue
		}
		key := TeacherHomepageFieldSelector{Field: field}
		if rule := p.rules[key]; rule.Override != nil {
			*homepageScalar(out, field) = redaction.clean(*rule.Override.Text, true)
		} else if p.allowed[key] {
			policy := redaction
			policy.keepOrdinary = field == "biography"
			*homepageScalar(out, field) = policy.clean(*homepageScalar(source, field), false)
		}
	}
	key := TeacherHomepageFieldSelector{Field: "supervisors"}
	if !p.hidden[key.Field] {
		if rule := p.rules[key]; rule.Override != nil {
			out.Supervisors = slices.Clone(*rule.Override.Supervisors)
		} else if p.allowed[key] {
			out.Supervisors = slices.Clone(source.Supervisors)
		}
	}
	for _, item := range boundHomepageItems(p.fieldItems(source.Fields, &redaction), teacherHomepageFieldLimit) {
		out.Fields = append(out.Fields, TeacherHomepageField{Label: item.label, Value: item.text})
	}
	sections := boundHomepageItems(p.sectionItems(source.Sections, &redaction), teacherHomepageSectionLimit)
	for _, item := range sections {
		out.Sections = append(out.Sections, TeacherHomepageSection{Title: item.label, Text: item.text})
	}
	if out.Biography != "" && p.rules[TeacherHomepageFieldSelector{Field: "biography"}].Override == nil {
		out.Biography = redaction.deduplicateVisibleBiography(out)
	}
	p.deduplicateSections(out, sections, placements)
	if allowed, override := p.photo(); allowed {
		if override != nil {
			out.Photo = &TeacherHomepagePhoto{ID: override.ID, ContentType: override.ContentType, Width: override.Width, Height: override.Height}
		} else if source.Photo != nil {
			photo := *source.Photo
			out.Photo = &photo
		}
		if photo := out.Photo; photo != nil {
			photo.URL = "/api/v1/catalog/objects/" + url.PathEscape(teacher) + "/teacher-homepage/photos/" + photo.ID
		}
	}
	if len(out.Fields) == 0 && len(out.Sections) == 0 && len(out.Supervisors) == 0 && out.Photo == nil &&
		!slices.ContainsFunc(teacherHomepageScalarFields, func(field string) bool { return *homepageScalar(out, field) != "" }) {
		return nil
	}
	normalizeTeacherHomepage(out)
	return out
}

func publishedTeacherHomepage(t Transaction, teacher Object) (*TeacherHomepage, error) {
	port, ok := t.(TeacherHomepageTransaction)
	if !ok || teacher.Kind != "teacher" {
		return nil, nil
	}
	presentation, err := readTeacherHomepagePresentation(t, teacher)
	if err != nil || !presentation.permitsPage() {
		return nil, err
	}
	id := teacherHomepageID(teacher)
	source, err := port.TeacherHomepage(teacher.SchoolID, id)
	if err != nil {
		return nil, err
	}
	if source != nil || presentation.controls.Independent {
		presentation.directory, err = teacherDisplayDirectory(t, teacher)
		if err != nil {
			return nil, err
		}
	}
	return presentation.profile(source, id), nil
}

func (s *Service) ReadTeacherHomepage(ctx context.Context, p Principal, id string) (TeacherHomepagePublication, error) {
	return s.readTeacherHomepage(ctx, p, id, false)
}

// ReadTeacherHomepageWithContacts handles a reader's explicit reveal. Both paths
// resolve the current match, reader scope and per-teacher controls in one read.
func (s *Service) ReadTeacherHomepageWithContacts(ctx context.Context, p Principal, id string) (TeacherHomepagePublication, error) {
	return s.readTeacherHomepage(ctx, p, id, true)
}

func (s *Service) readTeacherHomepage(ctx context.Context, p Principal, id string, reveal bool) (TeacherHomepagePublication, error) {
	var out TeacherHomepagePublication
	err := s.readPublicObject(ctx, p, id, true, func(t Transaction, teacher Object) error {
		if teacher.Kind != "teacher" {
			return ErrNotFound
		}
		profile, err := publishedTeacherHomepage(t, teacher)
		if err != nil {
			return err
		}
		if profile != nil {
			if !reveal {
				out.ContactsAvailable = maskHomepageContacts(profile)
			}
			out.Profile = &TeacherHomepagePublicProfile{TeacherHomepage: profile, Name: profile.Name}
		}
		return nil
	})
	return out, err
}
