#!/bin/bash
export MAVEN_OPTS=""
mvn dependency:purge-local-repository -DmanualInclude=no.nav.tjenestespesifikasjoner --settings maven-settings.xml
mvn dependency:go-offline -U --settings maven-settings.xml
