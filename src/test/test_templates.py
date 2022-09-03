from sprobot.templates import all_templates


def test_max_fields():
    for guild_id, templates in all_templates.items():
        for template in templates:
            assert len(template.Fields) <= 5


def test_no_duplicates():
    for guild_id, templates in all_templates.items():
        unique_templates = set()
        for template in templates:
            assert template.Name not in unique_templates
            unique_templates.add(template.Name)

            unique_fields = set()
            for field in template.Fields:
                assert field.Name not in unique_fields
                unique_fields.add(field.Name)


def test_all_forms_filled():
    for guild_id, templates in all_templates.items():
        for template in templates:
            assert template.Name != ""
            assert template.ShortName != ""
            assert template.Description != ""
            for field in template.Fields:
                assert field.Name != ""
                assert field.Placeholder != ""


def test_single_image():
    for guild_id, templates in all_templates.items():
        for template in templates:
            image_count = 0
            for field in template.Fields:
                if field.Image:
                    image_count += 1
            assert image_count == 1
