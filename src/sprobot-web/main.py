from flask import Flask

app = Flask(__name__)


@app.route("/")
def show_profile() -> str:
    return "Online profiles coming soon!"
