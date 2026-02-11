from flask import Flask, render_template
from s3 import S3Client

app = Flask(__name__)
s3_client = S3Client()

IMAGE_FIELD = "Gear Picture"


@app.route("/")
def index() -> str:
    return render_template("index.html")


@app.route("/<bucket>/profiles/<guild_id>/<template_name>/<user_id>.json")
def show_profile(bucket: str, guild_id: str, template_name: str, user_id: str) -> str:
    profile = s3_client.fetch_profile(guild_id, template_name, user_id)
    if profile is None:
        return render_template("404.html"), 404  # type: ignore[return-value]

    image_url = None
    fields = []
    for key, value in profile.items():
        if key == IMAGE_FIELD and value:
            image_url = value
        else:
            fields.append((key, value))

    return render_template(
        "profile.html",
        title=template_name,
        fields=fields,
        image_url=image_url,
    )


@app.errorhandler(404)
def page_not_found(e: Exception) -> tuple[str, int]:
    return render_template("404.html"), 404
