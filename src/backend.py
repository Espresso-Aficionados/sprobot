from typing import Dict
import tempfile
import shutil

from templates import Template

import requests
import filetype  # type: ignore

# import boto3
# client = boto3.client('s3', region_name='us-west-2')
# client.upload_file('images/image_0.jpg', 'mybucket', 'image_0.jpg')


def save_profile(template: Template, profile: Dict[str, str]) -> None:
    for field in template.Fields:
        if not field.Image:
            continue

        maybeURL = profile.get(field.Name, None)
        if not maybeURL:
            continue

        with tempfile.NamedTemporaryFile() as buf:
            # Save the (possible) image to a temp file
            r = requests.get(maybeURL, stream=True)
            r.raw.decode_content = True
            shutil.copyfileobj(r.raw, buf)
            buf.flush()

            # Try to verify that it is indeed an image
            kind = filetype.guess(buf.name)
            if not kind:
                del profile[field.Name]
                continue
            if not kind.mime.startswith("image/"):
                del profile[field.Name]
                continue

            # TODO: Upload that mofo to s3

            # Now we replace the original one with our new hosted URL
            profile[field.Name] = ""
