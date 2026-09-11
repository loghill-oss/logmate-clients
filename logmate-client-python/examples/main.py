import logmate


def main():
    logger = logmate.instrument(
        api_url="http://localhost:8080",
        sender_name="logmate-client-python-example",
    )
 
    # Log an info message with metadata
    logger.info(
        "application started",
        metadata={"environment": "development"},
    )

    # Log a warning message
    logger.warning("This is a warning message.")

    # Log with an event name
    logger.info("A simple event message", event="simple-event")

if __name__ == "__main__":
    main()
